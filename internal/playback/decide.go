package playback

import (
	"slices"

	"github.com/laterna-project/laterna/internal/domain"
)

// DeviceProfile is what a device says it can play. Each client builds it from its own capabilities
// (a browser probes them, an app knows them). The server assumes nothing.
type DeviceProfile struct {
	// Containers the device plays directly ("mp4", "mkv", "webm").
	Containers []string
	Video      []VideoSupport
	// AudioCodecs such as "aac", "opus", "flac", "ac3", "eac3", "mp3".
	AudioCodecs []string
	// HLS means the device can play HLS with fMP4 segments.
	HLS bool
	// SubtitleFormats the device renders itself ("vtt", "ass", "sup"). A selected subtitle it
	// cannot render is burned into the picture.
	SubtitleFormats []string
}

// VideoSupport describes a video codec the device can play.
type VideoSupport struct {
	Codec string
	// MaxBitDepth is the highest bit depth (8, 10...); 0 means 8.
	MaxBitDepth int
	// HDR means the device displays HDR. Otherwise an HDR source has to be tone mapped.
	HDR bool
}

// Method is how a file is played.
type Method string

// Playback methods, from cheapest to most expensive for the server.
const (
	// Direct serves the file as it is.
	Direct Method = "direct"
	// Remux is HLS without re-encoding.
	Remux Method = "remux"
	// Transcode is HLS with the video, the audio or both re-encoded.
	Transcode Method = "transcode"
	// Convert is for files without video (music): converted in one go to AAC in an MP4, then served
	// as a plain file.
	Convert Method = "convert"
	// Unplayable means there is no way to serve this file to this device (see Reasons).
	Unplayable Method = "unplayable"
)

// Transcode targets. Everything plays them.
const (
	TargetVideo = "h264"
	TargetAudio = "aac"
)

// Plan is the playback decision for a file on a device.
type Plan struct {
	Method Method
	// Video and Audio are the indexes of the streams played (Audio < 0: no sound).
	Video, Audio int
	// CopyVideo and CopyAudio mean the stream is copied as is; otherwise it is re-encoded to H.264
	// or AAC.
	CopyVideo, CopyAudio bool
	// ToneMap means an HDR picture is re-encoded and so must be converted to SDR (the 8-bit H.264
	// target has no HDR).
	ToneMap bool
	// Burn means the selected subtitle is burned into the picture, which is then re-encoded. Last
	// resort, when the device cannot render it.
	Burn bool
	// Reasons say why direct play (or remux) is not possible ("reason...." texts).
	Reasons []domain.Text
}

// Codecs that fit in fMP4 (HLS) without re-encoding.
var (
	fmp4Video = []string{"h264", "hevc", "av1", "vp9"}
	fmp4Audio = []string{"aac", "mp3", "opus", "flac", "ac3", "eac3", "alac"}
)

// Decide picks how a device plays a file: direct if it plays the container and the codecs,
// otherwise HLS without re-encoding if the codecs allow it, otherwise transcoding. audio is the
// index of the wanted audio stream (negative for the default one) and sub the subtitle shown from
// the start (nil for none). A subtitle the device renders itself changes nothing: it is served on
// the side and switching it restarts nothing.
func Decide(info domain.MediaInfo, dev DeviceProfile, audio int, sub *domain.Subtitle) Plan {
	p := Plan{Video: -1, Audio: -1}
	video, sound := pickStreams(info, audio)
	if video != nil {
		p.Video = video.Index
	}
	if sound != nil {
		p.Audio = sound.Index
	}

	videoOK := video != nil && supportsVideo(dev, *video)
	if video != nil && !videoOK {
		p.Reasons = append(p.Reasons, videoReason(*video))
	}
	audioOK := sound == nil || slices.Contains(dev.AudioCodecs, sound.Codec)
	if !audioOK {
		p.Reasons = append(p.Reasons, domain.T("reason.audio_unsupported", "codec", sound.Codec))
	}
	containerOK := slices.Contains(dev.Containers, info.Container)
	if !containerOK {
		p.Reasons = append(p.Reasons, domain.T("reason.container_unsupported", "container", info.Container))
	}
	if video == nil {
		return decideAudio(p, dev, sound, audioOK && containerOK)
	}

	// Each stream is copied if the device plays it and it fits in fMP4, otherwise it is re-encoded
	// to a target the device plays.
	p.CopyVideo = video == nil || (videoOK && slices.Contains(fmp4Video, video.Codec))
	p.CopyAudio = sound == nil || (audioOK && slices.Contains(fmp4Audio, sound.Codec))
	if sub != nil && video != nil && !ShowsSubtitle(dev, *sub) {
		p.Burn, p.CopyVideo = true, false
		p.Reasons = append(p.Reasons, domain.T("reason.subtitle_burned", "codec", sub.Codec))
	}
	p.ToneMap = video != nil && !p.CopyVideo && isHDR(*video)

	switch {
	case videoOK && audioOK && containerOK && !p.Burn:
		p.Method, p.Reasons, p.CopyVideo, p.CopyAudio, p.ToneMap = Direct, nil, true, true, false
	case !dev.HLS:
		p.Method = Unplayable
		p.Reasons = append(p.Reasons, domain.T("reason.no_hls"))
	case p.CopyVideo && p.CopyAudio:
		p.Method = Remux
	case !p.CopyVideo && !supportsVideo(dev, domain.Stream{Codec: TargetVideo, BitDepth: 8}):
		p.Method = Unplayable
		p.Reasons = append(p.Reasons, domain.T("reason.no_h264"))
	case !p.CopyAudio && !slices.Contains(dev.AudioCodecs, TargetAudio):
		p.Method = Unplayable
		p.Reasons = append(p.Reasons, domain.T("reason.no_aac"))
	default:
		p.Method = Transcode
	}
	return p
}

// pickStreams picks the video (the first one) and the audio to play: the stream with index audio,
// or when audio is negative the default one, or else the first.
func pickStreams(info domain.MediaInfo, audio int) (video, sound *domain.Stream) {
	for i := range info.Streams {
		s := &info.Streams[i]
		switch s.Kind {
		case domain.StreamVideo:
			if video == nil {
				video = s
			}
		case domain.StreamAudio:
			switch {
			case audio >= 0 && s.Index == audio:
				sound = s
			case audio < 0 && (sound == nil || (s.Default && !sound.Default)):
				sound = s
			}
		case domain.StreamSubtitle, domain.StreamAttachment, domain.StreamData:
		}
	}
	return video, sound
}

// decideAudio handles a file without video: as is if the device plays it, otherwise converted to
// AAC in an MP4. A whole file lets the device preload the next track and seek freely. Never HLS.
func decideAudio(p Plan, dev DeviceProfile, sound *domain.Stream, direct bool) Plan {
	switch {
	case sound == nil:
		p.Method = Unplayable
		p.Reasons = append(p.Reasons, domain.T("reason.no_streams"))
	case direct:
		p.Method, p.Reasons, p.CopyVideo, p.CopyAudio = Direct, nil, true, true
	case !slices.Contains(dev.AudioCodecs, TargetAudio) || (!slices.Contains(dev.Containers, "mp4") && !slices.Contains(dev.Containers, "m4a")):
		p.Method = Unplayable
		p.Reasons = append(p.Reasons, domain.T("reason.no_aac_mp4"))
	default:
		p.Method, p.CopyVideo, p.CopyAudio = Convert, true, false
	}
	return p
}

// ShowsSubtitle reports whether the device renders a subtitle itself, in one of the formats we
// serve.
func ShowsSubtitle(dev DeviceProfile, sub domain.Subtitle) bool {
	return slices.ContainsFunc(sub.Formats, func(f string) bool { return slices.Contains(dev.SubtitleFormats, f) })
}

func isHDR(s domain.Stream) bool { return s.DynamicRange != "" && s.DynamicRange != domain.SDR }

func supportsVideo(dev DeviceProfile, s domain.Stream) bool {
	depth := max(s.BitDepth, 8)
	hdr := isHDR(s)
	for _, v := range dev.Video {
		if v.Codec == s.Codec && depth <= max(v.MaxBitDepth, 8) && (!hdr || v.HDR) {
			return true
		}
	}
	return false
}

// videoReason says what the device cannot play in a video stream: codec, bit depth or dynamic
// range.
func videoReason(s domain.Stream) domain.Text {
	dynamic := s.DynamicRange
	if dynamic == "" {
		dynamic = domain.SDR
	}
	return domain.T("reason.video_unsupported", "codec", s.Codec, "bit_depth", max(s.BitDepth, 8), "dynamic_range", dynamic)
}
