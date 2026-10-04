package segments

import (
	"testing"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
)

func s(sec float64) time.Duration { return time.Duration(sec * float64(time.Second)) }

func TestFromChapters(t *testing.T) {
	cases := []struct {
		name     string
		chapters []domain.Chapter
		duration time.Duration
		want     []domain.Segment
	}{
		{
			name: "fansub",
			chapters: []domain.Chapter{
				{Start: 0, End: s(95), Title: "Prologue"},
				{Start: s(95), End: s(185), Title: "Opening"},
				{Start: s(185), End: s(1290), Title: "Part A"},
				{Start: s(1290), End: s(1380), Title: "ED"},
				{Start: s(1380), End: s(1410), Title: "Preview"},
			},
			duration: s(1410),
			want: []domain.Segment{
				{Kind: domain.SegmentIntro, Start: s(95), End: s(185)},
				{Kind: domain.SegmentCredits, Start: s(1290), End: s(1380)},
				{Kind: domain.SegmentPreview, Start: s(1380), End: s(1410)},
			},
		},
		{
			name: "French, bare \"Générique\" depends on where it is",
			chapters: []domain.Chapter{
				{Start: 0, End: s(40), Title: "Dans les épisodes précédents…"},
				{Start: s(40), End: s(80), Title: "Générique"},
				{Start: s(80), End: s(2500), Title: "Épisode"},
				{Start: s(2500), End: s(2600), Title: "Générique"},
			},
			duration: s(2600),
			want: []domain.Segment{
				{Kind: domain.SegmentRecap, Start: 0, End: s(40)},
				{Kind: domain.SegmentIntro, Start: s(40), End: s(80)},
				{Kind: domain.SegmentCredits, Start: s(2500), End: s(2600)},
			},
		},
		{
			name: "numbered, first of each kind, end clamped to the duration",
			chapters: []domain.Chapter{
				{Start: s(10), End: s(100), Title: "OP1"},
				{Start: s(100), End: s(200), Title: "Opening 2"},
				{Start: s(1300), End: s(1500), Title: "Ending Theme"},
			},
			duration: s(1400),
			want: []domain.Segment{
				{Kind: domain.SegmentIntro, Start: s(10), End: s(100)},
				{Kind: domain.SegmentCredits, Start: s(1300), End: s(1400)},
			},
		},
		{
			name: "nothing to skip",
			chapters: []domain.Chapter{
				{Start: 0, End: s(600), Title: "Chapter 1"},
				{Start: s(600), End: s(1200), Title: "Opération tonnerre"},
				{Start: s(1200), End: s(1300), Title: "Edition spéciale"},
				{Start: s(1300), End: s(1300.5), Title: "Intro"},
			},
			duration: s(1400),
		},
	}
	for _, c := range cases {
		got := FromChapters(c.chapters, c.duration)
		for i := range c.want {
			c.want[i].Source = domain.SegmentFromChapters
		}
		if len(got) != len(c.want) {
			t.Errorf("%s: %+v", c.name, got)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: %+v, want %+v", c.name, got[i], c.want[i])
			}
		}
	}
}

func TestWindows(t *testing.T) {
	if w := IntroWindow(s(1440)); w.Start != 0 || w.Length != s(432) {
		t.Errorf("intro of a 24 min episode: %+v", w)
	}
	if w := IntroWindow(s(3600)); w.Length != 10*time.Minute {
		t.Errorf("intro of a one-hour episode: %+v", w)
	}
	if w := CreditsWindow(s(1440)); w.Start != s(1080) || w.Length != s(360) {
		t.Errorf("credits of a 24 min episode: %+v", w)
	}
	if w := CreditsWindow(s(3600)); w.Start != s(3120) || w.Length != 8*time.Minute {
		t.Errorf("credits of a one-hour episode: %+v", w)
	}
}

func TestFromAudio(t *testing.T) {
	cases := []struct {
		kind       domain.SegmentKind
		start, end float64
		want       bool
	}{
		{domain.SegmentIntro, 60, 150, true},
		{domain.SegmentIntro, 60, 70, false},  // too short
		{domain.SegmentIntro, 60, 400, false}, // too long for an intro
		{domain.SegmentCredits, 1300, 1390, true},
		{domain.SegmentCredits, 1000, 1700, false},
		{domain.SegmentRecap, 0, 60, false}, // never from audio
	}
	for _, c := range cases {
		if got := FromAudio(c.kind, s(c.start), s(c.end), DefaultMin); got != c.want {
			t.Errorf("%s %v -> %v: %v", c.kind, c.start, c.end, got)
		}
	}
}
