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
	// Under a bitrate limit, VideoRate caps the re-encoded video and MaxHeight its height (0: no
	// cap, the playback default), and AudioRate is the bitrate of re-encoded audio, downmixed to
	// stereo (0: 64 kbit/s per channel, 6 at most). All in kbit/s; a converted track (Convert)
	// gets AudioRate too.
	VideoRate, AudioRate int
	MaxHeight            int
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
//
// limit is the highest bitrate of the stream in kbit/s, 0 for none. A file above it has its video
// re-encoded under the limit (a music track is converted), and so does any re-encoded video. A
// file below it plays as it would without one.
func Decide(info domain.MediaInfo, dev DeviceProfile, audio int, sub *domain.Subtitle, limit int) Plan {
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
	over := limit > 0 && info.Bitrate/1000 > int64(limit)
	if over {
		p.Reasons = append(p.Reasons, domain.T("reason.bitrate_above_limit", "bitrate_kbps", info.Bitrate/1000, "limit_kbps", limit))
	}
	if video == nil {
		return decideAudio(p, dev, sound, audioOK && containerOK, over, limit)
	}

	// Each stream is copied if the device plays it and it fits in fMP4, otherwise it is re-encoded
	// to a target the device plays.
	p.CopyVideo = video == nil || (videoOK && slices.Contains(fmp4Video, video.Codec))
	p.CopyAudio = sound == nil || (audioOK && slices.Contains(fmp4Audio, sound.Codec))
	if sub != nil && video != nil && !ShowsSubtitle(dev, *sub) {
		p.Burn, p.CopyVideo = true, false
		p.Reasons = append(p.Reasons, domain.T("reason.subtitle_burned", "codec", sub.Codec))
	}
	if over {
		p.CopyVideo = false
	}
	if limit > 0 && !p.CopyVideo {
		p.share(limit, dev, sound)
	}
	p.ToneMap = video != nil && !p.CopyVideo && isHDR(*video)

	switch {
	case videoOK && audioOK && containerOK && !p.Burn && !over:
		p.Method, p.Reasons, p.CopyVideo, p.CopyAudio, p.ToneMap = Direct, nil, true, true, false
		p.VideoRate, p.AudioRate, p.MaxHeight = 0, 0, 0
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

// AudioStream is the audio stream a playback plays (see pickStreams); nil if the file has none.
func AudioStream(info domain.MediaInfo, audio int) *domain.Stream {
	_, sound := pickStreams(info, audio)
	return sound
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
// AAC in an MP4. A whole file lets the device preload the next track and seek freely. Never HLS. A
// track above the bitrate limit is converted under it, unless the device only plays it as it is.
func decideAudio(p Plan, dev DeviceProfile, sound *domain.Stream, direct, over bool, limit int) Plan {
	convertible := slices.Contains(dev.AudioCodecs, TargetAudio) && (slices.Contains(dev.Containers, "mp4") || slices.Contains(dev.Containers, "m4a"))
	switch {
	case sound == nil:
		p.Method = Unplayable
		p.Reasons = append(p.Reasons, domain.T("reason.no_streams"))
	case direct && (!over || !convertible):
		p.Method, p.Reasons, p.CopyVideo, p.CopyAudio = Direct, nil, true, true
	case !convertible:
		p.Method = Unplayable
		p.Reasons = append(p.Reasons, domain.T("reason.no_aac_mp4"))
	default:
		p.Method, p.CopyVideo, p.CopyAudio = Convert, true, false
		if over {
			p.AudioRate = min(max(limit*9/10, minAudioRate), musicRate)
		}
	}
	return p
}

// Rates under a bitrate limit, in kbit/s.
const (
	// minVideoRate is the least a re-encoded video gets, however low the limit.
	minVideoRate = 200
	// minAudioRate and maxAudioRate bound re-encoded stereo audio.
	minAudioRate = 64
	maxAudioRate = 192
	// musicRate is the bitrate of a converted music track (as without a limit).
	musicRate = 256
)

// share splits a bitrate limit (kbit/s) between a re-encoded video and its audio. Audio that would
// be copied keeps its rate if it takes a small share of the limit; otherwise it is re-encoded, to
// stereo when the limit is tight. The video gets the rest, minus a tenth: encoders overshoot their
// target by a few percent, and the container and forced keyframes cost some more. Its height is
// the one that rate carries well in H.264.
func (p *Plan) share(limit int, dev DeviceProfile, sound *domain.Stream) {
	audio := 0
	if sound != nil {
		channels := min(max(sound.Channels, 2), 6)
		if p.CopyAudio && streamRate(*sound) > limit/4 && slices.Contains(dev.AudioCodecs, TargetAudio) {
			p.CopyAudio = false
		}
		switch {
		case p.CopyAudio:
			audio = streamRate(*sound)
		case 64*channels <= limit/8:
			audio = 64 * channels
		default:
			p.AudioRate = min(max(limit/16, minAudioRate), maxAudioRate)
			audio = p.AudioRate
		}
	}
	p.VideoRate = max(limit*9/10-audio, minVideoRate)
	switch {
	case p.VideoRate >= 5000:
		p.MaxHeight = 1080
	case p.VideoRate >= 2500:
		p.MaxHeight = 720
	case p.VideoRate >= 1000:
		p.MaxHeight = 480
	default:
		p.MaxHeight = 360
	}
}

// streamRate is the bitrate of an audio stream in kbit/s, guessed from its codec when the file does
// not say (Matroska often does not).
func streamRate(s domain.Stream) int {
	if s.Bitrate > 0 {
		return int(s.Bitrate / 1000)
	}
	channels := max(s.Channels, 2)
	switch s.Codec {
	case "aac", "mp3", "opus", "vorbis":
		return 64 * channels
	case "ac3", "eac3":
		return 640
	}
	return 500 * channels // lossless or unknown
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
