package transcode

import "strconv"

// FileOptions describes how to make a whole file for an offline download: each stream is copied or
// re-encoded as for playback, but from one end of the file to the other and into an MP4 that plays
// without a network.
type FileOptions struct {
	Path, Output string
	// Audio is the index of the audio stream; negative for no sound.
	Audio int
	// CopyVideo means the video is copied as is; otherwise it is re-encoded to H.264 with Encoder.
	CopyVideo bool
	// VideoTag is the tag of the copied video ("hvc1" for HEVC).
	VideoTag string
	Encoder  Encoder
	// MaxHeight caps the height of the re-encoded video.
	MaxHeight int
	// VideoRate caps the bitrate of the re-encoded video (kbit/s), for encoders that can do it
	// without leaving their quality mode; 0 means no cap.
	VideoRate int
	// ToneMap is the HDR to SDR conversion (nil for an SDR source). GPU runs the chain on the
	// graphics card.
	ToneMap *ToneMapper
	GPU     bool
	// CopyAudio means the audio is copied as is; otherwise it is re-encoded to AAC.
	CopyAudio bool
	// Channels of the source stream. Re-encoded audio keeps 6 at most.
	Channels int
	// AudioRate is the bitrate of the re-encoded audio (kbit/s), downmixed to stereo since 5.1
	// would sound bad at these rates. 0 means the playback one (64 per channel, 6 channels at
	// most).
	AudioRate int
}

// FileArgs builds the FFmpeg command for a whole file. Progress is written to standard output
// ("-progress", out_time_us=...). Unlike playback runs, B-frames are allowed: nothing is cut into
// segments and the file comes out smaller.
func FileArgs(o FileOptions) []string {
	args := []string{"-nostdin", "-hide_banner", "-v", "error", "-nostats", "-progress", "pipe:1"}
	gpu := o.GPU && !o.CopyVideo
	if !o.CopyVideo {
		args = append(args, o.Encoder.device...)
		switch {
		case gpu:
			args = append(args, gpuDevice...)
		case o.ToneMap != nil:
			args = append(args, o.ToneMap.device...)
		}
	}
	args = append(args, "-i", "file:"+o.Path, "-map", "0:V:0")
	if o.Audio >= 0 {
		args = append(args, "-map", "0:"+strconv.Itoa(o.Audio))
	}
	if o.CopyVideo {
		args = append(args, "-c:v", "copy")
		if o.VideoTag != "" {
			args = append(args, "-tag:v", o.VideoTag)
		}
	} else {
		if gpu {
			args = append(args, "-vf", gpuFilter(o.Encoder, o.MaxHeight, o.ToneMap != nil))
		} else {
			args = append(args, "-vf", videoFilter(o.Encoder, o.MaxHeight, o.ToneMap))
		}
		args = append(append(args, "-c:v", o.Encoder.Name), o.Encoder.quality...)
		if o.VideoRate > 0 && o.Encoder.capped {
			rate := strconv.Itoa(o.VideoRate)
			args = append(args, "-maxrate", rate+"k", "-bufsize", strconv.Itoa(2*o.VideoRate)+"k")
		}
		if o.ToneMap != nil {
			args = append(args, "-color_primaries", "bt709", "-color_trc", "bt709", "-colorspace", "bt709")
		}
	}
	if o.Audio >= 0 {
		if o.CopyAudio {
			args = append(args, "-c:a", "copy")
		} else {
			ch := min(max(o.Channels, 2), 6)
			rate := o.AudioRate
			if rate == 0 {
				rate = 64 * ch
			} else {
				ch = 2
			}
			args = append(args, "-c:a", "aac", "-ac", strconv.Itoa(ch), "-b:a", strconv.Itoa(rate)+"k")
		}
	}
	return append(args, "-map_chapters", "-1", "-map_metadata", "-1", "-movflags", "+faststart", "-f", "mp4", "-y", "file:"+o.Output)
}
