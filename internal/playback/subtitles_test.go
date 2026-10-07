package playback

import (
	"testing"

	"github.com/laterna-project/laterna/internal/domain"
)

func TestPickSubtitle(t *testing.T) {
	text := []string{"vtt"}
	image := []string{"sup"}
	subs := []domain.Subtitle{
		{Position: 0, Language: "eng", Formats: text},
		{Position: 1, Language: "fre", Forced: true, Formats: text},
		{Position: 2, Language: "fre", HearingImpaired: true, Formats: text},
		{Position: 3, Language: "fre", Formats: image},
		{Position: 4, Language: "fre", Formats: text},
	}
	cases := []struct {
		name        string
		subs        []domain.Subtitle
		mode        domain.SubtitleMode
		lang, audio string
		want        int // -1: none
	}{
		{"automatic, foreign audio: whole, text, no notes", subs, domain.SubtitleAuto, "fre", "eng", 4},
		{"automatic, audio in the language: forced only", subs, domain.SubtitleAuto, "fre", "fre", 1},
		{"automatic, audio says nothing: forced only", subs, domain.SubtitleAuto, "fre", "", 1},
		{"automatic, no forced one: none", subs[2:], domain.SubtitleAuto, "fre", "fre", -1},
		{"always, even with audio in the language", subs, domain.SubtitleAlways, "fre", "fre", 4},
		{"forced, foreign audio: still forced only", subs, domain.SubtitleForced, "fre", "eng", 1},
		{"off", subs, domain.SubtitleOff, "fre", "eng", -1},
		{"no language known", subs, domain.SubtitleAlways, "", "eng", -1},
		{"none in the language", subs, domain.SubtitleAlways, "ger", "eng", -1},
		{"only an image one: taken", []domain.Subtitle{{Position: 0, Language: "fre", Formats: image}}, domain.SubtitleAuto, "fre", "eng", 0},
		{"only a forced one when a whole one is wanted: taken", subs[:2], domain.SubtitleAuto, "fre", "eng", 1},
		{"the file's default first", []domain.Subtitle{
			{Position: 0, Language: "fre", Formats: text},
			{Position: 1, Language: "fre", Default: true, Formats: text},
		}, domain.SubtitleAuto, "fre", "eng", 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := PickSubtitle(c.subs, c.mode, c.lang, c.audio)
			if !ok {
				got = -1
			}
			if got != c.want {
				t.Errorf("got %d, want %d", got, c.want)
			}
		})
	}
}
