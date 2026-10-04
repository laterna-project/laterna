package transcode

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/laterna-project/laterna/internal/media/fmp4"
	"github.com/laterna-project/laterna/internal/testfixtures"
)

func detect(t *testing.T) (string, Caps) {
	t.Helper()
	testfixtures.Library(t) // skips the test without FFmpeg
	ffmpeg, _, _ := testfixtures.FFmpeg()
	caps, err := Detect(context.Background(), ffmpeg)
	if err != nil {
		t.Fatal(err)
	}
	return ffmpeg, caps
}

func TestDetect(t *testing.T) {
	_, caps := detect(t)
	t.Logf("usable encoders: %v", caps.Names())
	names := caps.Names()
	// Software is always there (GPL builds) and always the last resort.
	if !slices.Contains(names, "libx264") || names[len(names)-1] != "libx264" {
		t.Errorf("libx264 missing or not last: %v", names)
	}
	if best, ok := caps.Best(); !ok || best.Name != names[0] {
		t.Errorf("best encoder: %v", best)
	}
	// Forced encoder: first if it works here, refused otherwise.
	if forced, ok := caps.Prefer("libx264"); !ok || forced.Names()[0] != "libx264" || len(forced.Names()) != len(names) {
		t.Errorf("forced libx264: %v", forced.Names())
	}
	if _, ok := caps.Prefer("h264_inconnu"); ok {
		t.Error("unknown encoder forced")
	}
	if !slices.Equal(caps.Names(), names) {
		t.Errorf("Prefer changed the encoders: %v", caps.Names())
	}
}

// fragments collects the fragment starts of a transcoded run.
func fragments(t *testing.T, ffmpeg string, o Options) []time.Duration {
	t.Helper()
	var starts []time.Duration
	err := fmp4.Run(context.Background(), fmp4.Command{Bin: ffmpeg, Args: Args(o)}, func([]byte) error { return nil },
		func(f fmp4.Fragment) error { starts = append(starts, f.Start); return nil })
	if err != nil {
		t.Fatal(err)
	}
	return starts
}

// source30 writes a 30 s source at 23.976 fps (a frame grid that does not line up with the 6 s
// boundaries, like a real movie) with a keyframe every 10 s: a run decodes from the previous
// keyframe. With or without sound.
func source30(t *testing.T, ffmpeg string, sound bool) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source.mkv")
	args := []string{"-v", "error", "-f", "lavfi", "-i", "testsrc2=s=320x180:r=24000/1001:d=30"}
	if sound {
		args = append(args, "-f", "lavfi", "-i", "sine=f=440:d=30", "-c:a", "aac")
	}
	args = append(args, "-c:v", "libx264", "-preset", "ultrafast", "-g", "240", "file:"+path)
	if out, err := exec.Command(ffmpeg, args...).CombinedOutput(); err != nil {
		t.Fatalf("source: %v %s", err, out)
	}
	return path
}

// An IDR keyframe opens each 6 s segment, at most one frame after the boundary, wherever the run
// starts (seek), and timestamps stay those of the source (with or without sound). Checked for every
// encoder usable here, not only the preferred one.
func TestKeyframesOnSegmentBoundaries(t *testing.T) {
	ffmpeg, caps := detect(t)
	const frame = time.Second * 1001 / 24000
	for _, sound := range []bool{true, false} {
		path := source30(t, ffmpeg, sound)
		audio := -1
		if sound {
			audio = 1
		}
		for _, enc := range caps.Encoders {
			t.Run(fmt.Sprintf("%s/son=%v", enc.Name, sound), func(t *testing.T) {
				for _, start := range []time.Duration{0, 6 * time.Second, 12 * time.Second, 18 * time.Second} {
					starts := fragments(t, ffmpeg, Options{
						Path: path, Start: start, Audio: audio, Encoder: enc, MaxHeight: 1080, Segment: 6 * time.Second, Channels: 1,
					})
					for want := start; want < 30*time.Second; want += 6 * time.Second {
						found := slices.ContainsFunc(starts, func(s time.Duration) bool { return s >= want && s < want+2*frame })
						if !found {
							t.Errorf("run from %v: no keyframe at %v; fragments %v", start, want, starts)
						}
					}
				}
			})
		}
	}
}

// Audio re-encoded to AAC: the encoder priming (1024 samples) stamps no packet before zero in a
// run that starts at the beginning. Media3 (ExoPlayer) refuses a negative tfdt ("Top bit not
// zero"); hls.js tolerates it. A run started by a seek keeps the same shift.
func TestEncodedAudioNeverBeforeZero(t *testing.T) {
	ffmpeg, _ := detect(t)
	path := source30(t, ffmpeg, true)
	_, ffprobe, _ := testfixtures.FFmpeg()
	first := func(start time.Duration) float64 {
		args := Args(Options{Path: path, Start: start, Audio: 1, CopyVideo: true, Channels: 2, Segment: 6 * time.Second})
		out := filepath.Join(t.TempDir(), "run.mp4")
		args = append(args[:len(args)-1], out) // a file instead of pipe:1
		if b, err := exec.Command(ffmpeg, args...).CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, b)
		}
		b, err := exec.Command(ffprobe, "-v", "error", "-select_streams", "a:0", "-show_entries", "packet=dts_time",
			"-of", "csv=p=0", "-read_intervals", "%+#1", out).Output()
		if err != nil {
			t.Fatal(err)
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(string(b)), 64)
		if err != nil {
			t.Fatalf("%q: %v", b, err)
		}
		return v
	}
	if at := first(0); at < 0 {
		t.Errorf("run from the start: first audio packet at %v s", at)
	}
	if at := first(12 * time.Second); at < 12-0.03 || at > 12+0.05 {
		t.Errorf("run at 12 s: first audio packet at %v s", at)
	}
}

// Video re-encoded, audio copied: the audio of the run starts with it, not at the source keyframe
// before it (here 10 s, for a run at 12 s).
func TestCopiedAudioStartsWithRun(t *testing.T) {
	ffmpeg, caps := detect(t)
	enc, _ := caps.Best()
	path := source30(t, ffmpeg, true)
	args := Args(Options{Path: path, Start: 12 * time.Second, Audio: 1, CopyAudio: true, Encoder: enc, Segment: 6 * time.Second})
	out := filepath.Join(t.TempDir(), "run.mp4")
	args = append(args[:len(args)-1], out) // a file instead of pipe:1
	if b, err := exec.Command(ffmpeg, args...).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, b)
	}
	_, ffprobe, _ := testfixtures.FFmpeg()
	b, err := exec.Command(ffprobe, "-v", "error", "-select_streams", "a:0", "-show_entries", "packet=pts_time",
		"-of", "csv=p=0", "-read_intervals", "%+#1", out).Output()
	if err != nil {
		t.Fatal(err)
	}
	// Tolerance: one AAC frame (1024 samples at 44.1 kHz, 23 ms) straddling the start.
	if first, err := strconv.ParseFloat(strings.TrimSpace(string(b)), 64); err != nil || first < 12-0.03 {
		t.Errorf("audio copied from %q, run at 12 s (%v)", b, err)
	}
}

func TestArgs(t *testing.T) {
	x264, _ := ByName("libx264")
	enc := strings.Join(Args(Options{Path: "a.mkv", Start: 12 * time.Second, Audio: 1, Encoder: x264, MaxHeight: 1080, Segment: 6 * time.Second, Channels: 8}), " ")
	for _, want := range []string{
		"-ss 12.000000", "-vf scale=-2:'min(ih,1080)',format=yuv420p", "-c:v libx264", "-crf 21", "-bf 0", "-fps_mode passthrough",
		"-force_key_frames expr:gte(t,n_forced*6.000000)", "-af asetpts=PTS+1024/SR/TB -c:a aac -ac 6 -b:a 384k", "negative_cts_offsets",
	} {
		if !strings.Contains(enc, want) {
			t.Errorf("transcode without %q: %s", want, enc)
		}
	}
	// Video copied, audio re-encoded (E-AC-3 to AAC): no video filter and no forced keyframe.
	audioOnly := strings.Join(Args(Options{Path: "a.mkv", Audio: 2, CopyVideo: true, VideoTag: "hvc1", Channels: 6}), " ")
	if !strings.Contains(audioOnly, "-c:v copy -tag:v hvc1") || strings.Contains(audioOnly, "-vf") ||
		strings.Contains(audioOnly, "force_key_frames") || !strings.Contains(audioOnly, "-c:a aac -ac 6") {
		t.Errorf("audio only re-encoded: %s", audioOnly)
	}
	vaapi, _ := ByName("h264_vaapi")
	if a := strings.Join(Args(Options{Path: "a.mkv", Audio: -1, Encoder: vaapi, Segment: 6 * time.Second}), " "); !strings.HasPrefix(a, "-nostdin -hide_banner -v error -vaapi_device /dev/dri/renderD128") ||
		!strings.Contains(a, "format=nv12,hwupload") || strings.Contains(a, "-c:a") {
		t.Errorf("VAAPI: %s", a)
	}
	if _, ok := ByName("inconnu"); ok {
		t.Error("unknown encoder accepted")
	}
}

// Burn-in (last resort): a filter graph instead of -vf, output [v].
func TestArgsBurn(t *testing.T) {
	x264, _ := ByName("libx264")
	base := Options{Path: "a.mkv", Start: 600 * time.Second, Audio: 1, CopyAudio: true, Encoder: x264, MaxHeight: 1080, Segment: 6 * time.Second}
	tail := "scale=-2:'min(ih,1080)',format=yuv420p[v]"
	cases := []struct {
		name string
		burn Burn
		want []string
	}{
		{
			"Blu-ray PGS over a cropped movie: bars put back",
			Burn{File: "s/4.sup", Width: 1920, Height: 1080, VideoWidth: 1920, VideoHeight: 802},
			[]string{"-i file:a.mkv -i file:s/4.sup -filter_complex", "[0:V:0]pad=1920:1080:(ow-iw)/2:(oh-ih)/2[pad];[pad][1:s:0]overlay=eof_action=pass," + tail},
		},
		{
			"same size",
			Burn{File: "s/4.sup", Width: 1920, Height: 1080, VideoWidth: 1920, VideoHeight: 1080},
			[]string{"[0:V:0][1:s:0]overlay=eof_action=pass," + tail},
		},
		{
			"VobSub smaller than the video",
			Burn{File: "s/2.mks", Width: 720, Height: 480, VideoWidth: 1440, VideoHeight: 1080},
			[]string{"[1:s:0]scale=1440:1080[sub];[0:V:0][sub]overlay=eof_action=pass," + tail},
		},
		{
			"text: libass and fonts, relative paths",
			Burn{Text: "burn.ass", FontsDir: "polices"},
			[]string{"-i file:a.mkv -filter_complex [0:V:0]subtitles=f=burn.ass:fontsdir=polices," + tail},
		},
	}
	for _, c := range cases {
		o := base
		o.Burn = &c.burn
		args := strings.Join(Args(o), " ")
		for _, want := range append(c.want, "-map [v] -map 0:1", "-c:v libx264", "-copypriorss:a 0") {
			if !strings.Contains(args, want) {
				t.Errorf("%s: without %q\n%s", c.name, want, args)
			}
		}
		if strings.Contains(args, "-vf") || strings.Contains(args, "0:V:0 -map") {
			t.Errorf("%s: -vf or video mapped directly\n%s", c.name, args)
		}
	}
	// Video copied: burn-in is not possible, nothing changes.
	o := base
	o.CopyVideo, o.Burn = true, &Burn{File: "s/4.sup"}
	if args := strings.Join(Args(o), " "); strings.Contains(args, "filter_complex") || !strings.Contains(args, "-map 0:V:0") {
		t.Errorf("video copied: %s", args)
	}
}
