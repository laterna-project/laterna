// Package segments finds the parts of an episode a player may offer to skip: from the named
// chapters of the file, or from the audio two episodes of a season have in common (where to look,
// and which lengths to accept). Pure logic, no FFmpeg and no database.
package segments

import (
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/laterna-project/laterna/internal/domain"
)

// DefaultMin is the shortest intro or credits accepted from audio. Anything shorter is more likely
// a jingle or a sound effect.
const DefaultMin = 15 * time.Second

// Maximum lengths. A longer part is not what it claims to be (background music reused from one
// episode to the next, a misplaced "OP" chapter). Chapters are named by hand, so they are trusted
// for longer.
const (
	maxIntro          = 3 * time.Minute
	maxCredits        = 10 * time.Minute
	maxChapter        = 10 * time.Minute
	maxCreditsChapter = 20 * time.Minute
)

// Window is the part of an episode where intro or credits are searched by audio.
type Window struct{ Start, Length time.Duration }

// IntroWindow is the first third of the episode, 10 minutes at most (the intro sometimes follows a
// long cold open).
func IntroWindow(duration time.Duration) Window {
	return Window{Length: min(duration*3/10, 10*time.Minute)}
}

// CreditsWindow is the last quarter of the episode, 8 minutes at most.
func CreditsWindow(duration time.Duration) Window {
	length := min(duration/4, 8*time.Minute)
	return Window{Start: duration - length, Length: length}
}

// FromAudio reports whether audio shared by two episodes, from start to end, makes a segment of the
// wanted kind (SegmentIntro or SegmentCredits): at least minLength and not too long.
func FromAudio(kind domain.SegmentKind, start, end, minLength time.Duration) bool {
	d := end - start
	switch kind {
	case domain.SegmentIntro:
		return d >= minLength && d <= maxIntro
	case domain.SegmentCredits:
		return d >= minLength && d <= maxCredits
	case domain.SegmentRecap, domain.SegmentPreview:
	}
	return false
}

// Chapter names, lower case and without accents, followed by the end of the title, a space or a
// digit ("OP1", "Opening 2"). The first match wins, so the most specific come first.
var chapterNames = []struct {
	kind  domain.SegmentKind
	names []string
}{
	{domain.SegmentCredits, []string{"generique de fin", "generique fin", "end credits", "closing credits", "ending", "credits", "credit", "outro", "ed"}},
	{domain.SegmentIntro, []string{"generique de debut", "generique debut", "opening credits", "opening", "title sequence", "main titles", "main title", "theme song", "introduction", "intro", "op"}},
	{domain.SegmentRecap, []string{"dans les episodes precedents", "previously on", "previously", "precedemment", "recap", "resume"}},
	{domain.SegmentPreview, []string{"prochain episode", "next episode", "next time", "a suivre", "preview", "apercu"}},
}

// FromChapters returns the segments named by the chapters of a file, ordered by start: the first
// chapter of each kind. A bare "Générique" is the intro in the first half of the file and the
// credits after that.
func FromChapters(chapters []domain.Chapter, duration time.Duration) []domain.Segment {
	var out []domain.Segment
	for _, c := range chapters {
		kind, ok := chapterKind(fold(c.Title), c.Start, duration)
		if !ok || slices.ContainsFunc(out, func(s domain.Segment) bool { return s.Kind == kind }) {
			continue
		}
		end := c.End
		if duration > 0 {
			end = min(end, duration)
		}
		limit := maxChapter
		if kind == domain.SegmentCredits {
			limit = maxCreditsChapter
		}
		if d := end - c.Start; c.Start < 0 || d < time.Second || d > limit {
			continue
		}
		out = append(out, domain.Segment{Kind: kind, Start: c.Start, End: end, Source: domain.SegmentFromChapters})
	}
	slices.SortFunc(out, func(a, b domain.Segment) int { return int(a.Start - b.Start) })
	return out
}

func chapterKind(title string, start, duration time.Duration) (domain.SegmentKind, bool) {
	for _, group := range chapterNames {
		for _, name := range group.names {
			if hasWord(title, name) {
				return group.kind, true
			}
		}
	}
	if hasWord(title, "generique") {
		if duration > 0 && start >= duration/2 {
			return domain.SegmentCredits, true
		}
		return domain.SegmentIntro, true
	}
	return "", false
}

// hasWord reports whether title starts with name followed by nothing, a space or a digit.
func hasWord(title, name string) bool {
	rest, ok := strings.CutPrefix(title, name)
	return ok && (rest == "" || rest[0] == ' ' || rest[0] >= '0' && rest[0] <= '9')
}

// fold lower-cases a title, strips accents, replaces punctuation with spaces and collapses spaces.
func fold(title string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(title) {
		switch {
		case strings.ContainsRune("àâäá", r):
			r = 'a'
		case strings.ContainsRune("éèêë", r):
			r = 'e'
		case strings.ContainsRune("îïí", r):
			r = 'i'
		case strings.ContainsRune("ôöó", r):
			r = 'o'
		case strings.ContainsRune("ùûüú", r):
			r = 'u'
		case r == 'ç':
			r = 'c'
		case !unicode.IsLetter(r) && !unicode.IsDigit(r):
			r = ' '
		}
		b.WriteRune(r)
	}
	return strings.Join(strings.Fields(b.String()), " ")
}
