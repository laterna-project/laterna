// Package transcode builds the FFmpeg commands for transcoded playback and detects which H.264
// encoders really work on the machine: each encoder FFmpeg lists is tried (one second of a test
// pattern), hardware first, software as a last resort. Nothing is assumed about the hardware: if a
// card is missing, a driver is not there or a test fails, the next encoder takes over.
package transcode

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/laterna-project/laterna/internal/proc"
)

// Encoder describes an H.264 encoder and how to use it.
type Encoder struct {
	// Name is the FFmpeg name ("h264_nvenc", "libx264"...).
	Name string
	// Hardware means a hardware encoder (software, on the CPU, otherwise).
	Hardware bool
	// device holds the init arguments that go before the input (VAAPI device...).
	device []string
	// upload means frames must be sent to the device before the encoder (hwupload filter).
	upload bool
	// pixel is the pixel format given to the encoder.
	pixel string
	// quality holds the quality settings (aiming close to x264 CRF 21 to 23) and makes forced
	// keyframes IDR frames (independent segments).
	quality []string
	// capped means the encoder can cap the bitrate (-maxrate, -bufsize) without leaving this
	// quality mode (downloaded files).
	capped bool
}

// encoders in order of preference. Hardware takes load off the CPU; software (libx264, always there
// in GPL builds) makes sure transcoding works everywhere.
var encoders = []Encoder{
	{Name: "h264_nvenc", Hardware: true, pixel: "yuv420p", quality: []string{"-preset", "p4", "-rc", "vbr", "-cq", "23", "-b:v", "0", "-forced-idr", "1", "-profile:v", "high"}, capped: true},
	{Name: "h264_qsv", Hardware: true, pixel: "nv12", quality: []string{"-preset", "veryfast", "-global_quality", "23", "-forced_idr", "1", "-profile:v", "high"}},
	{Name: "h264_amf", Hardware: true, pixel: "nv12", quality: []string{"-quality", "balanced", "-rc", "cqp", "-qp_i", "21", "-qp_p", "23", "-qp_b", "25", "-profile:v", "high"}},
	{Name: "h264_vaapi", Hardware: true, device: []string{"-vaapi_device", "/dev/dri/renderD128"}, upload: true, pixel: "nv12", quality: []string{"-qp", "23", "-profile:v", "high"}},
	{Name: "h264_videotoolbox", Hardware: true, pixel: "nv12", quality: []string{"-q:v", "65", "-profile:v", "high"}},
	{Name: "h264_v4l2m2m", Hardware: true, pixel: "yuv420p", quality: []string{"-b:v", "8M"}},
	{Name: "h264_mf", Hardware: true, pixel: "nv12", quality: []string{"-rate_control", "quality", "-quality", "70", "-hw_encoding", "1"}},
	{Name: "libx264", pixel: "yuv420p", quality: []string{"-preset", "veryfast", "-crf", "21", "-profile:v", "high"}, capped: true},
}

// Caps is what the machine can encode.
type Caps struct {
	// Encoders are the H.264 encoders that passed the test, in order of preference.
	Encoders []Encoder
	// ToneMappers are the HDR to SDR conversions that passed the test with the best encoder
	// (DetectToneMappers), in order of preference.
	ToneMappers []ToneMapper
	// GPU means the chain on the card passed the test with the best encoder (DetectGPU).
	GPU bool
}

// BestToneMapper returns the preferred HDR to SDR conversion; false if none works.
func (c Caps) BestToneMapper() (ToneMapper, bool) {
	if len(c.ToneMappers) == 0 {
		return ToneMapper{}, false
	}
	return c.ToneMappers[0], true
}

// Best returns the preferred H.264 encoder; false if none works.
func (c Caps) Best() (Encoder, bool) {
	if len(c.Encoders) == 0 {
		return Encoder{}, false
	}
	return c.Encoders[0], true
}

// Names returns the names of the usable encoders (log, console).
func (c Caps) Names() []string {
	out := make([]string, len(c.Encoders))
	for i, e := range c.Encoders {
		out[i] = e.Name
	}
	return out
}

// Prefer returns the encoders with name first; false if it is not usable here.
func (c Caps) Prefer(name string) (Caps, bool) {
	i := slices.IndexFunc(c.Encoders, func(e Encoder) bool { return e.Name == name })
	if i < 0 {
		return c, false
	}
	out := append([]Encoder{c.Encoders[i]}, slices.Delete(slices.Clone(c.Encoders), i, i+1)...)
	return Caps{Encoders: out}, true
}

// Known returns the names of the H.264 encoders we know, in order of preference.
func Known() []string { return Caps{Encoders: encoders}.Names() }

// Detect tries each H.264 encoder FFmpeg lists and keeps those that work.
func Detect(ctx context.Context, ffmpeg string) (Caps, error) {
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}
	out, err := proc.Command(ctx, ffmpeg, "-hide_banner", "-encoders").Output()
	if err != nil {
		return Caps{}, fmt.Errorf("transcode: listing encoders: %w", err)
	}
	listed := map[string]bool{}
	for line := range strings.Lines(string(out)) {
		if f := strings.Fields(line); len(f) >= 2 && strings.HasPrefix(f[0], "V") {
			listed[f[1]] = true
		}
	}
	var caps Caps
	for _, e := range encoders {
		if !listed[e.Name] {
			continue
		}
		if err := try(ctx, ffmpeg, e); err == nil {
			caps.Encoders = append(caps.Encoders, e)
		}
	}
	if len(caps.Encoders) == 0 {
		return caps, errors.New("transcode: no usable H.264 encoder")
	}
	return caps, nil
}

// try encodes one second of a test pattern with e, the way playback would (same filter chain).
func try(ctx context.Context, ffmpeg string, e Encoder) error {
	args := append([]string{"-hide_banner", "-v", "error", "-nostdin"}, e.device...)
	args = append(args, "-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=24:duration=1",
		"-vf", videoFilter(e, 0, nil))
	args = append(args, e.encode()...)
	args = append(args, "-f", "null", "-")
	if err := trial(ctx, 20*time.Second, ffmpeg, args); err != nil {
		return fmt.Errorf("%s: %w", e.Name, err)
	}
	return nil
}

// encode returns the encoder, its quality settings and no B-frames. Without B-frames the decode
// order is the display order: decode timestamps equal those of the source wherever the run starts,
// and two segments from different runs follow each other without going back in time. With B-frames
// the reordering delay depends on the first frames of the run and a player may drop the keyframe at
// the junction. The cost is a slightly higher bitrate for the same quality, which does not matter
// on a local network.
func (e Encoder) encode() []string {
	return append(append([]string{"-c:v", e.Name}, e.quality...), "-bf", "0")
}

// videoFilter scales to maxHeight (0 keeps the original size), converts HDR to SDR if tm is not nil
// (after scaling: four times fewer pixels to convert for a 4K source), converts to the encoder's
// format (8 bits: a 10-bit source is converted) and uploads to the device if the encoder needs it.
func videoFilter(e Encoder, maxHeight int, tm *ToneMapper) string {
	var parts []string
	if maxHeight > 0 {
		parts = append(parts, "scale=-2:'min(ih,"+strconv.Itoa(maxHeight)+")'")
	}
	if tm != nil {
		parts = append(parts, tm.filter)
	}
	return strings.Join(append(parts, encoderFilter(e)), ",")
}

// encoderFilter converts to the encoder's format and uploads to the device if it needs it.
func encoderFilter(e Encoder) string {
	if e.upload {
		return "format=" + e.pixel + ",hwupload"
	}
	return "format=" + e.pixel
}

// Options describes one transcoded playback run. Each stream is copied or re-encoded.
type Options struct {
	Path string
	// Start of the run (the start of a segment).
	Start time.Duration
	// Audio is the index of the audio stream; negative for no sound.
	Audio int
	// CopyVideo means the video is copied as is; otherwise it is re-encoded to H.264 with Encoder.
	CopyVideo bool
	// VideoTag is the tag of the copied video ("hvc1" for HEVC).
	VideoTag string
	Encoder  Encoder
	// MaxHeight caps the height of the re-encoded video.
	MaxHeight int
	// Segment is the segment duration. An IDR keyframe is forced at the start of each one
	// (re-encoded video).
	Segment time.Duration
	// CopyAudio means the audio is copied as is; otherwise it is re-encoded to AAC.
	CopyAudio bool
	// Channels of the source audio stream. Re-encoded audio keeps 6 at most.
	Channels int
	// Burn is a subtitle burned into the re-encoded picture (last resort); nil otherwise.
	Burn *Burn
	// ToneMap is the HDR to SDR conversion of the re-encoded picture; nil for an SDR source.
	ToneMap *ToneMapper
	// GPU means the picture is decoded, scaled (and converted to SDR) on the card. ToneMap then
	// only says a conversion is needed and libplacebo does it. No effect with Burn.
	GPU bool
}

// Burn describes a subtitle to burn in.
type Burn struct {
	// File is an extracted image subtitle (.sup, .mks), read as a second input and from its start,
	// so that a line that began before the run started is known. Read from the stream of the media
	// file, -ss would have skipped it.
	File string
	// Text is a text subtitle (ASS, WebVTT), rendered by libass with the fonts in FontsDir. Both
	// paths are relative to FFmpeg's working directory (a filter cannot read a Windows path without
	// escaping).
	Text, FontsDir string
	// Width and Height are the canvas size of the image subtitle (0 if unknown). VideoWidth and
	// VideoHeight are the size of the video.
	Width, Height, VideoWidth, VideoHeight int
}

// graph builds the filter graph for burn-in. It ends with the [v] output.
func (b Burn) graph(tail string) string {
	if b.Text != "" {
		fonts := ""
		if b.FontsDir != "" {
			fonts = ":fontsdir=" + b.FontsDir
		}
		return "[0:V:0]subtitles=f=" + b.Text + fonts + "," + tail + "[v]"
	}
	sub := "[1:s:0]"
	video := "[0:V:0]"
	var pre []string
	switch {
	case b.Width <= 0 || b.Height <= 0 || b.VideoWidth <= 0 || b.VideoHeight <= 0 ||
		(b.Width == b.VideoWidth && b.Height == b.VideoHeight):
	case b.Width >= b.VideoWidth && b.Height >= b.VideoHeight:
		// A cropped movie (black bars removed) under subtitles from the Blu-ray: the bars come
		// back, so subtitles that sat in them stay visible.
		pre = append(pre, fmt.Sprintf("[0:V:0]pad=%d:%d:(ow-iw)/2:(oh-ih)/2[pad]", b.Width, b.Height))
		video = "[pad]"
	default:
		pre = append(pre, fmt.Sprintf("%sscale=%d:%d[sub]", sub, b.VideoWidth, b.VideoHeight))
		sub = "[sub]"
	}
	return strings.Join(append(pre, video+sub+"overlay=eof_action=pass,"+tail+"[v]"), ";")
}

// Args builds the FFmpeg command (an argument slice, never a shell; "file:" input). The output is
// the same as a remux: a continuous fMP4 stream, fragments on keyframes, original timestamps.
func Args(o Options) []string {
	args := []string{"-nostdin", "-hide_banner", "-v", "error"}
	burn := o.Burn != nil && !o.CopyVideo
	gpu := o.GPU && !o.CopyVideo && !burn
	if !o.CopyVideo {
		args = append(args, o.Encoder.device...)
		switch {
		case gpu:
			args = append(args, gpuDevice...)
		case o.ToneMap != nil:
			args = append(args, o.ToneMap.device...)
		}
	}
	args = append(args, "-copyts", "-ss", seconds(o.Start), "-i", "file:"+o.Path)
	switch {
	case burn && o.Burn.Text == "":
		args = append(args, "-i", "file:"+o.Burn.File) // no -ss: read from the start
		fallthrough
	case burn:
		args = append(args, "-filter_complex", o.Burn.graph(videoFilter(o.Encoder, o.MaxHeight, o.ToneMap)), "-map", "[v]")
	default:
		args = append(args, "-map", "0:V:0")
	}
	if o.Audio >= 0 {
		args = append(args, "-map", "0:"+strconv.Itoa(o.Audio))
	}
	if o.CopyVideo {
		args = append(args, "-c:v", "copy")
		if o.VideoTag != "" {
			args = append(args, "-tag:v", o.VideoTag)
		}
	} else {
		switch {
		case gpu:
			args = append(args, "-vf", gpuFilter(o.Encoder, o.MaxHeight, o.ToneMap != nil))
		case !burn:
			args = append(args, "-vf", videoFilter(o.Encoder, o.MaxHeight, o.ToneMap))
		}
		args = append(args, o.Encoder.encode()...)
		if o.ToneMap != nil {
			// Converted picture: tag it as BT.709 SDR, or a player would still think it is HDR.
			args = append(args, "-color_primaries", "bt709", "-color_trc", "bt709", "-colorspace", "bt709")
		}
		// Source timestamps as they are (no frame duplicated or dropped) and a keyframe forced at
		// the start of each segment. The t in the expression counts from the first encoded frame,
		// even with -copyts: the run starts on a segment boundary and the next ones are at n_forced
		// x segment. That first frame is at most one frame after the boundary (accurate -ss), and
		// so is a keyframe, never before.
		args = append(args, "-fps_mode", "passthrough", "-g", "600", "-force_key_frames",
			"expr:gte(t,n_forced*"+strconv.FormatFloat(o.Segment.Seconds(), 'f', 6, 64)+")")
	}
	if o.Audio >= 0 {
		if o.CopyAudio {
			args = append(args, "-c:a", "copy")
			if !o.CopyVideo {
				// FFmpeg reads from the source keyframe before the start. Video decoded before the
				// start is dropped, but copied audio would be kept (up to one GOP of audio
				// duplicated with the previous segment).
				args = append(args, "-copypriorss:a", "0")
			}
		} else {
			ch := min(max(o.Channels, 2), 6)
			// The AAC encoder adds 1024 samples of priming, which would be stamped before the start of
			// the run. The audio is shifted by that much (21 ms at 48 kHz, inaudible), the same way in
			// every run. Media3 refuses a negative decode time (tfdt); hls.js tolerates it.
			args = append(args, "-af", "asetpts=PTS+1024/SR/TB",
				"-c:a", "aac", "-ac", strconv.Itoa(ch), "-b:a", strconv.Itoa(64*ch)+"k")
		}
	}
	return append(args,
		"-map_chapters", "-1", "-map_metadata", "-1", "-avoid_negative_ts", "disabled",
		"-movflags", "+frag_keyframe+empty_moov+default_base_moof+frag_discont+negative_cts_offsets",
		"-f", "mp4", "pipe:1")
}

func seconds(d time.Duration) string { return strconv.FormatFloat(d.Seconds(), 'f', 6, 64) }

// ByName returns the encoder with that name if it is one we know (forced setting).
func ByName(name string) (Encoder, bool) {
	i := slices.IndexFunc(encoders, func(e Encoder) bool { return e.Name == name })
	if i < 0 {
		return Encoder{}, false
	}
	return encoders[i], true
}
