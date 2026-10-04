// Package playback decides and describes playback without doing any I/O: HLS segmenting, the
// playlist, and the choice of a playback method from the device and the source.
package playback

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// TargetSegment is the duration an HLS segment aims for.
const TargetSegment = 6 * time.Second

// Segment is an HLS playback range, from Start (a keyframe, or the beginning) to End.
type Segment struct {
	Start, End time.Duration
}

// Segments cuts a source into segments that each start on a keyframe: a boundary is placed at the
// first keyframe found at least target after the previous one. The first segment starts at zero and
// the last ends at duration. The cut depends only on the source, so it is the same for every
// playback and a single segment can be produced on its own after a seek.
func Segments(keyframes []time.Duration, duration, target time.Duration) []Segment {
	if duration <= 0 {
		return nil
	}
	starts := []time.Duration{0}
	for _, k := range keyframes {
		if k >= duration {
			break
		}
		if k-starts[len(starts)-1] >= target {
			starts = append(starts, k)
		}
	}
	segs := make([]Segment, len(starts))
	for i, s := range starts {
		end := duration
		if i+1 < len(starts) {
			end = starts[i+1]
		}
		segs[i] = Segment{Start: s, End: end}
	}
	return segs
}

// FixedSegments cuts a duration into segments of a fixed length, for re-encoded video where a
// keyframe is forced at the start of each segment. A remainder shorter than half a segment is added
// to the last one: a file is as long as its longest stream, often the audio, and a segment starting
// after the last frame would have no picture at all.
func FixedSegments(duration, length time.Duration) []Segment {
	if duration <= 0 || length <= 0 {
		return nil
	}
	segs := make([]Segment, 0, duration/length+1)
	for start := time.Duration(0); start < duration; start += length {
		if n := len(segs); n > 0 && duration-start < length/2 {
			segs[n-1].End = duration
			break
		}
		segs = append(segs, Segment{Start: start, End: min(start+length, duration)})
	}
	return segs
}

// Find returns the index of the segment that contains t (the last one starting at or before t), or
// -1 without segments.
func Find(segs []Segment, t time.Duration) int {
	if len(segs) == 0 {
		return -1
	}
	lo, hi := 0, len(segs)-1
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if segs[mid].Start <= t {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo
}

// Playlist writes the full HLS VOD playlist (fMP4): the init segment, then every segment.
func Playlist(segs []Segment, initURI string, segmentURI func(i int) string) string {
	var longest time.Duration
	for _, s := range segs {
		longest = max(longest, s.End-s.Start)
	}
	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:7\n")
	fmt.Fprintf(&b, "#EXT-X-TARGETDURATION:%d\n", int(math.Ceil(longest.Seconds())))
	b.WriteString("#EXT-X-PLAYLIST-TYPE:VOD\n#EXT-X-INDEPENDENT-SEGMENTS\n")
	fmt.Fprintf(&b, "#EXT-X-MAP:URI=%q\n", initURI)
	for i, s := range segs {
		fmt.Fprintf(&b, "#EXTINF:%.6f,\n%s\n", (s.End - s.Start).Seconds(), segmentURI(i))
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	return b.String()
}
