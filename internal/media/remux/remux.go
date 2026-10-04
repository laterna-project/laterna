// Package remux builds the FFmpeg command that produces a fragmented MP4 stream from a point in the
// source without re-encoding. fmp4.Run runs it and delivers the fragments.
package remux

import (
	"strconv"
	"time"
)

// Options describes one run.
type Options struct {
	Path string
	// Start is where the run begins, a keyframe of the source. FFmpeg starts from the index point
	// just before it, and the earlier fragments are to be thrown away.
	Start time.Duration
	// Audio is the index of the audio stream in the file; negative for no audio.
	Audio int
	// VideoTag forces the tag of the video track: "hvc1" for HEVC, which Safari and AVPlayer
	// require over HLS (FFmpeg writes "hev1" by default). Empty keeps FFmpeg's.
	VideoTag string
}

// Args builds the FFmpeg command (an argument slice, never a shell; "file:" input).
func Args(o Options) []string {
	args := []string{
		"-nostdin", "-hide_banner", "-v", "error",
		"-copyts", "-ss", strconv.FormatFloat(o.Start.Seconds(), 'f', 6, 64), "-i", "file:" + o.Path,
		"-map", "0:V:0",
	}
	if o.Audio >= 0 {
		args = append(args, "-map", "0:"+strconv.Itoa(o.Audio))
	}
	// Signed composition offsets (negative_cts_offsets): the original presentation times are kept
	// without an edit list, so video does not drift from audio when there are B-frames. No chapters
	// and no metadata, which would add a text track.
	if o.VideoTag != "" {
		args = append(args, "-tag:v", o.VideoTag)
	}
	return append(args,
		"-map_chapters", "-1", "-map_metadata", "-1",
		"-c", "copy", "-avoid_negative_ts", "disabled",
		"-movflags", "+frag_keyframe+empty_moov+default_base_moof+frag_discont+negative_cts_offsets",
		"-f", "mp4", "pipe:1")
}
