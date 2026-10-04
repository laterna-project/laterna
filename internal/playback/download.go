package playback

import (
	"slices"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
)

// Offline downloads work like playback: the device says what it plays, and the server hands over
// the file as it is when it can, otherwise a whole MP4 (H.264 video, AAC audio) that copies
// everything the device plays and the requested quality allows. Never HLS: a file can be kept,
// resumed after an interruption and played without a network.

// quality describes a download quality: maximum height and bitrates (kbit/s; 0 means no cap, the
// playback setting applies).
type quality struct {
	maxHeight int
	video     int
	audio     int
	// music is the bitrate of a converted music track.
	music int
}

// qualities: the original is only scaled down if it has to be re-encoded anyway (1080p at most,
// like playback). The others aim for a maximum size.
var qualities = map[domain.DownloadQuality]quality{
	domain.DownloadOriginal: {maxHeight: 1080},
	domain.DownloadHigh:     {maxHeight: 1080, video: 8000, audio: 192, music: 256},
	domain.DownloadMedium:   {maxHeight: 720, video: 4000, audio: 160, music: 160},
	domain.DownloadLow:      {maxHeight: 480, video: 1500, audio: 128, music: 96},
}

// nominal is the typical video bitrate (kbit/s) of an uncapped re-encode at a given height. It is
// used to estimate the size of a re-encoded original.
func nominal(height int) int {
	switch {
	case height > 720:
		return 6000
	case height > 480:
		return 3000
	}
	return 1200
}

// DownloadPlan is the download decision for a file on a device.
type DownloadPlan struct {
	// Method is Direct (the file as is), Remux (MP4 without re-encoding), Transcode (MP4 partly or
	// fully re-encoded), Convert (music: AAC) or Unplayable.
	Method Method
	// Video and Audio are the indexes of the streams kept (negative: none).
	Video, Audio int
	// CopyVideo and CopyAudio mean the stream is copied as is; otherwise it is re-encoded to H.264
	// or AAC.
	CopyVideo, CopyAudio bool
	// ToneMap means an HDR picture is re-encoded and must be converted to SDR.
	ToneMap bool
	// MaxHeight caps the height of the re-encoded picture. VideoRate and AudioRate cap the bitrates
	// (kbit/s; 0 means the playback setting).
	MaxHeight            int
	VideoRate, AudioRate int
	// Estimate is the expected size of the delivered file, in bytes.
	Estimate int64
	// Reasons say why the file is not delivered as is ("reason...." texts).
	Reasons []domain.Text
}

// DecideDownload picks how to deliver a file to a device at a given quality. size is the size of
// the original file. A file the device plays and that is not heavier than the requested quality is
// delivered as is: we never re-encode for nothing.
func DecideDownload(info domain.MediaInfo, size int64, dev DeviceProfile, audio int, q domain.DownloadQuality) DownloadPlan {
	spec, ok := qualities[q]
	if !ok {
		spec = qualities[domain.DownloadOriginal]
	}
	p := DownloadPlan{Video: -1, Audio: -1}
	video, sound := pickStreams(info, audio)
	if video != nil {
		p.Video = video.Index
	}
	if sound != nil {
		p.Audio = sound.Index
	}
	audioOK := sound == nil || slices.Contains(dev.AudioCodecs, sound.Codec)
	if !audioOK {
		p.Reasons = append(p.Reasons, domain.T("reason.audio_unsupported", "codec", sound.Codec))
	}
	containerOK := slices.Contains(dev.Containers, info.Container)
	if !containerOK {
		p.Reasons = append(p.Reasons, domain.T("reason.container_unsupported", "container", info.Container))
	}
	mp4 := slices.Contains(dev.Containers, "mp4") || slices.Contains(dev.Containers, "m4a")
	aac := slices.Contains(dev.AudioCodecs, TargetAudio)
	bitrate := int(info.Bitrate / 1000)

	if video == nil {
		switch {
		case sound == nil:
			p.Method = Unplayable
			p.Reasons = append(p.Reasons, domain.T("reason.no_streams"))
		case audioOK && containerOK && (spec.music == 0 || (bitrate > 0 && bitrate <= spec.music)):
			p.Method, p.Reasons, p.CopyAudio, p.Estimate = Direct, nil, true, size
		case !aac || !mp4:
			p.Method = Unplayable
			p.Reasons = append(p.Reasons, domain.T("reason.no_aac_mp4"))
		default:
			if audioOK && containerOK {
				p.Reasons = append(p.Reasons, domain.T("reason.bitrate_above_quality"))
			}
			p.Method, p.AudioRate = Convert, spec.music
			rate := spec.music
			if rate == 0 {
				rate = 256 // same as the playback conversion (stereo)
			}
			p.Estimate = estimate(info.Duration, 0, rate)
		}
		return p
	}

	videoOK := supportsVideo(dev, *video)
	if !videoOK {
		p.Reasons = append(p.Reasons, videoReason(*video))
	}
	// Does the file fit the requested quality? The original always does.
	fits := q == domain.DownloadOriginal ||
		(video.Height <= spec.maxHeight && bitrate > 0 && bitrate <= spec.video+spec.audio)
	if !fits {
		p.Reasons = append(p.Reasons, domain.T("reason.larger_than_quality"))
	}
	if videoOK && audioOK && containerOK && fits {
		p.Method, p.Reasons, p.CopyVideo, p.CopyAudio, p.Estimate = Direct, nil, true, true, size
		return p
	}
	if !mp4 {
		p.Method = Unplayable
		p.Reasons = append(p.Reasons, domain.T("reason.no_mp4"))
		return p
	}
	// Each stream is copied if the device plays it and it fits both MP4 and the quality; otherwise
	// it is re-encoded.
	p.CopyVideo = videoOK && slices.Contains(fmp4Video, video.Codec) &&
		(q == domain.DownloadOriginal || (video.Height <= spec.maxHeight && bitrate > 0 && bitrate <= spec.video+spec.audio))
	p.CopyAudio = sound == nil || (audioOK && slices.Contains(fmp4Audio, sound.Codec) &&
		(q == domain.DownloadOriginal || q == domain.DownloadHigh))
	switch {
	case !p.CopyVideo && !supportsVideo(dev, domain.Stream{Codec: TargetVideo, BitDepth: 8}):
		p.Method = Unplayable
		p.Reasons = append(p.Reasons, domain.T("reason.no_h264"))
		return p
	case !p.CopyAudio && !aac:
		p.Method = Unplayable
		p.Reasons = append(p.Reasons, domain.T("reason.no_aac"))
		return p
	}
	p.Method = Transcode
	if p.CopyVideo && p.CopyAudio {
		p.Method = Remux
	}
	videoRate, audioRate := 0, 0
	if !p.CopyVideo {
		p.ToneMap = isHDR(*video)
		p.MaxHeight, p.VideoRate = spec.maxHeight, spec.video
		height := min(video.Height, spec.maxHeight)
		if height == 0 {
			height = spec.maxHeight
		}
		videoRate = spec.video
		if videoRate == 0 {
			videoRate = nominal(height)
		}
	}
	if !p.CopyAudio && sound != nil {
		p.AudioRate = spec.audio
		audioRate = spec.audio
		if audioRate == 0 {
			audioRate = 64 * min(max(sound.Channels, 2), 6)
		}
	}
	switch {
	case p.Method == Remux:
		p.Estimate = size
	case p.CopyVideo && sound != nil:
		// Only the audio is re-encoded: the video keeps its weight.
		p.Estimate = max(0, size-estimate(info.Duration, 0, int(sound.Bitrate/1000))) + estimate(info.Duration, 0, audioRate)
	default:
		p.Estimate = estimate(info.Duration, videoRate, audioRate)
	}
	return p
}

// estimate returns the size of a file of that duration at those bitrates (kbit/s).
func estimate(d time.Duration, videoRate, audioRate int) int64 {
	return int64(d.Seconds() * float64(videoRate+audioRate) * 1000 / 8)
}
