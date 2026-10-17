package playback

import (
	"testing"

	"github.com/laterna-project/laterna/internal/domain"
)

func TestPickAudio(t *testing.T) {
	audio := func(index int, lang string, channels int, def bool, title string) domain.Stream {
		return domain.Stream{Index: index, Kind: domain.StreamAudio, Language: lang, Channels: channels, Default: def, Title: title}
	}
	streams := []domain.Stream{
		{Index: 0, Kind: domain.StreamVideo},
		audio(1, "fre", 6, true, ""),
		audio(2, "jpn", 2, false, "Director's commentary"),
		audio(3, "jpn", 2, false, ""),
		audio(4, "jpn", 6, false, ""),
		audio(5, "eng", 2, false, ""),
		audio(6, "eng", 2, true, ""),
	}
	cases := []struct {
		name string
		lang string
		want int // -1: none
	}{
		{"the most channels, the commentary last", "jpn", 4},
		{"the file's default first", "eng", 6},
		{"only one", "fre", 1},
		{"none in that language", "ger", -1},
		{"no language", "", -1},
	}
	for _, c := range cases {
		got, ok := PickAudio(streams, c.lang)
		if !ok {
			got = -1
		}
		if got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
	if got, ok := PickAudio([]domain.Stream{audio(1, "jpn", 2, false, "Commentaire audio")}, "jpn"); !ok || got != 1 {
		t.Errorf("only a commentary: %d %v", got, ok)
	}
}

func TestPickSameSubtitle(t *testing.T) {
	text := []string{"vtt"}
	subs := []domain.Subtitle{
		{Position: 0, Language: "eng", Formats: text},
		{Position: 1, Language: "fre", Forced: true, Formats: text},
		{Position: 2, Language: "fre", HearingImpaired: true, Formats: text},
		{Position: 3, Language: "fre", Formats: text},
	}
	cases := []struct {
		name   string
		subs   []domain.Subtitle
		lang   string
		forced bool
		want   int
	}{
		{"whole, as before", subs, "fre", false, 3},
		{"forced, as before", subs, "fre", true, 1},
		{"no forced one this time: a whole one", subs[2:], "fre", true, 3},
		{"no whole one this time: the forced one", subs[:2], "fre", false, 1},
		{"none in the language", subs, "ger", false, -1},
	}
	for _, c := range cases {
		got, ok := PickSameSubtitle(c.subs, c.lang, c.forced)
		if !ok {
			got = -1
		}
		if got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
}
