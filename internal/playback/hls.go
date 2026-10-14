// Package playback decides and describes playback without doing any I/O: HLS segmenting, the
// playlist, and the choice of a playback method from the device and the source.
package playback

import (
	"bytes"
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

// Rendition is a subtitle offered by the master playlist as a WebVTT rendition: what a player that
// shows subtitles only from the stream (Safari's own HLS player on iPhone) offers in its menu.
type Rendition struct {
	// Name labels the subtitle in the player's menu; a client finds the subtitle by it.
	Name string
	// Language is a BCP 47 tag ("fr"), empty if unknown.
	Language string
	// HearingImpaired marks captions for the deaf and hard of hearing (sounds described too).
	HearingImpaired bool
	// URI of the subtitle's playlist.
	URI string
}

// MasterPlaylist writes the HLS master playlist of a playback: the subtitles as renditions, then
// the only variant, mediaURI, of bandwidth bit/s at most. No subtitle is shown by default: the
// client picks one.
func MasterPlaylist(mediaURI string, bandwidth int64, subs []Rendition) string {
	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-INDEPENDENT-SEGMENTS\n")
	for _, s := range subs {
		fmt.Fprintf(&b, `#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID="subs",NAME="%s",DEFAULT=NO,AUTOSELECT=YES`, Quotable(s.Name))
		if s.Language != "" {
			fmt.Fprintf(&b, `,LANGUAGE="%s"`, Quotable(s.Language))
		}
		if s.HearingImpaired {
			b.WriteString(`,CHARACTERISTICS="public.accessibility.transcribes-spoken-dialog,public.accessibility.describes-music-and-sound"`)
		}
		fmt.Fprintf(&b, ",URI=\"%s\"\n", Quotable(s.URI))
	}
	fmt.Fprintf(&b, "#EXT-X-STREAM-INF:BANDWIDTH=%d", max(bandwidth, 1))
	if len(subs) > 0 {
		b.WriteString(`,SUBTITLES="subs"`)
	}
	fmt.Fprintf(&b, "\n%s\n", mediaURI)
	return b.String()
}

// Quotable makes s fit a quoted attribute of a playlist, as a player will read it: HLS has no
// escape, so a double quote becomes a single one and line breaks and other control characters go.
func Quotable(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '"':
			return '\''
		case r < ' ' || r == 0x7f:
			return -1
		}
		return r
	}, s)
}

// SubtitlePlaylist writes the playlist of a WebVTT rendition: one segment, the whole subtitle, for
// the whole duration.
func SubtitlePlaylist(duration time.Duration, segmentURI string) string {
	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:3\n")
	fmt.Fprintf(&b, "#EXT-X-TARGETDURATION:%d\n", max(int(math.Ceil(duration.Seconds())), 1))
	b.WriteString("#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-PLAYLIST-TYPE:VOD\n")
	fmt.Fprintf(&b, "#EXTINF:%.6f,\n%s\n#EXT-X-ENDLIST\n", duration.Seconds(), segmentURI)
	return b.String()
}

// timestampMap ties the times of a WebVTT segment to those of the media. Subtitles and segments
// both keep the times of the source (-copyts): zero is zero.
const timestampMap = "X-TIMESTAMP-MAP=MPEGTS:0,LOCAL:00:00:00.000"

// HLSWebVTT makes a WebVTT segment of an HLS rendition from a WebVTT file: the timestamp map goes
// into its header, right after the "WEBVTT" line.
func HLSWebVTT(vtt []byte) []byte {
	if bytes.Contains(vtt, []byte("X-TIMESTAMP-MAP=")) {
		return vtt
	}
	first, rest, _ := bytes.Cut(vtt, []byte("\n"))
	if !bytes.HasPrefix(bytes.TrimPrefix(first, []byte("\ufeff")), []byte("WEBVTT")) {
		return append([]byte("WEBVTT\n"+timestampMap+"\n\n"), vtt...)
	}
	out := make([]byte, 0, len(vtt)+len(timestampMap)+2)
	out = append(out, first...)
	out = append(out, '\n')
	out = append(out, timestampMap...)
	if bytes.HasSuffix(first, []byte("\r")) {
		out = append(out, '\r')
	}
	out = append(out, '\n')
	return append(out, rest...)
}
