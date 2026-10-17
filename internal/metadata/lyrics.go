package metadata

import (
	"cmp"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
)

// Lyrics: LRC files and the lyrics in tags. LRC gives each line the times it is sung at
// ("[01:12.30]"), possibly several, and some header tags ("[ar:...]", "[offset:+250]"); enhanced
// LRC also times each word ("<01:12.30>"), which is dropped. Text without any time is plain lyrics.

// maxLyricLines caps the lines kept: lyrics, not a book.
const maxLyricLines = 5000

var (
	lrcTime   = regexp.MustCompile(`^\[(\d{1,3}):(\d{1,2})(?:[.:](\d{1,3}))?\]`)
	lrcHeader = regexp.MustCompile(`^\[([a-zA-Z#]+):([^\]]*)\]\s*$`)
	lrcWord   = regexp.MustCompile(`<\d{1,3}:\d{1,2}(?:[.:]\d{1,3})?>`)
)

// ParseLyrics reads lyrics, LRC or plain. Synced lyrics come sorted by time; plain ones keep their
// lines, a single empty line between stanzas.
func ParseLyrics(text string) (lines []domain.LyricLine, synced bool) {
	text = strings.TrimPrefix(strings.ToValidUTF8(text, "�"), "\ufeff")
	var offset time.Duration
	var timed, plain []domain.LyricLine
	for raw := range strings.Lines(text) {
		line := strings.TrimSpace(raw)
		if m := lrcHeader.FindStringSubmatch(line); m != nil && !lrcTime.MatchString(line) {
			if strings.EqualFold(m[1], "offset") {
				if ms, err := strconv.Atoi(strings.TrimSpace(m[2])); err == nil {
					offset = time.Duration(ms) * time.Millisecond
				}
			}
			continue
		}
		var starts []time.Duration
		for {
			m := lrcTime.FindStringSubmatch(line)
			if m == nil {
				break
			}
			starts = append(starts, lrcDuration(m[1], m[2], m[3]))
			line = line[len(m[0]):]
		}
		line = strings.TrimSpace(spacesRe.ReplaceAllString(lrcWord.ReplaceAllString(line, ""), " "))
		if len(starts) == 0 {
			if line != "" || (len(plain) > 0 && plain[len(plain)-1].Text != "") {
				plain = append(plain, domain.LyricLine{Text: line})
			}
			continue
		}
		for _, s := range starts {
			timed = append(timed, domain.LyricLine{Start: s, Text: line})
		}
	}
	if len(timed) == 0 {
		for len(plain) > 0 && plain[len(plain)-1].Text == "" {
			plain = plain[:len(plain)-1]
		}
		return capLines(plain), false
	}
	// A positive offset shows the lines sooner.
	for i := range timed {
		timed[i].Start = max(timed[i].Start-offset, 0)
	}
	slices.SortStableFunc(timed, func(a, b domain.LyricLine) int { return cmp.Compare(a.Start, b.Start) })
	return capLines(timed), true
}

// lrcDuration reads "mm", "ss" and the fraction of a second ("3" is 300 ms, "30" 300 ms, "300"
// too).
func lrcDuration(minutes, seconds, frac string) time.Duration {
	m, _ := strconv.Atoi(minutes)
	s, _ := strconv.Atoi(seconds)
	d := time.Duration(m)*time.Minute + time.Duration(s)*time.Second
	if frac != "" {
		f, _ := strconv.Atoi(frac)
		for i := len(frac); i < 3; i++ {
			f *= 10
		}
		d += time.Duration(f) * time.Millisecond
	}
	return d
}

func capLines(lines []domain.LyricLine) []domain.LyricLine {
	if len(lines) > maxLyricLines {
		return lines[:maxLyricLines]
	}
	return lines
}

// LyricsTag returns the lyrics in the tags of an audio file (as ffprobe gives them, lower-case
// keys): LYRICS or UNSYNCEDLYRICS (Vorbis, MP4), "lyrics-eng" and the like (ID3 USLT); "" without
// any.
func LyricsTag(tags map[string]string) string {
	for _, k := range []string{"lyrics", "unsyncedlyrics", "unsynced lyrics", "syncedlyrics"} {
		if v := strings.TrimSpace(tags[k]); v != "" {
			return v
		}
	}
	keys := make([]string, 0, len(tags))
	for k := range tags {
		if strings.HasPrefix(k, "lyrics-") || strings.HasPrefix(k, "lyrics_") {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	for _, k := range keys {
		if v := strings.TrimSpace(tags[k]); v != "" {
			return v
		}
	}
	return ""
}
