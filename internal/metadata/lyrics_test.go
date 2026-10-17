package metadata

import (
	"testing"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
)

func TestParseLyrics(t *testing.T) {
	sec := func(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }
	lrc := "\ufeff[ar:Artist Test]\r\n[ti:Track One]\r\n[offset:+500]\r\n" +
		"[00:12.30]First line\r\n" +
		"[00:20.5][01:02.000]Chorus, twice\r\n" +
		"[00:15.00]<00:15.00>Second <00:16.20>line\r\n" +
		"[00:25]\r\n" +
		"not timed: dropped\r\n"
	lines, synced := ParseLyrics(lrc)
	want := []domain.LyricLine{
		{Start: sec(11.8), Text: "First line"},
		{Start: sec(14.5), Text: "Second line"},
		{Start: sec(20), Text: "Chorus, twice"},
		{Start: sec(24.5), Text: ""},
		{Start: sec(61.5), Text: "Chorus, twice"},
	}
	if !synced || len(lines) != len(want) {
		t.Fatalf("LRC: %v %+v", synced, lines)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("line %d: %+v, want %+v", i, lines[i], want[i])
		}
	}

	plain, synced := ParseLyrics("Verse one\nstill verse one\n\n\n\nVerse two\n\n")
	if synced || len(plain) != 4 || plain[2].Text != "" || plain[3].Text != "Verse two" {
		t.Errorf("plain: %v %+v", synced, plain)
	}
	if lines, _ := ParseLyrics(""); len(lines) != 0 {
		t.Errorf("empty: %+v", lines)
	}
	// A negative offset shows the lines later; nothing starts before 0.
	if lines, _ := ParseLyrics("[offset:-1000]\n[00:01.00]a\n[offset:+5000]"); len(lines) != 1 || lines[0].Start != 0 {
		t.Errorf("offset: %+v", lines)
	}
}

func TestLyricsTag(t *testing.T) {
	for _, c := range []struct {
		tags map[string]string
		want string
	}{
		{map[string]string{"lyrics": "Words"}, "Words"},
		{map[string]string{"unsyncedlyrics": " Words "}, "Words"},
		{map[string]string{"lyrics-eng": "English", "lyrics-fra": "French"}, "English"},
		{map[string]string{"title": "No words"}, ""},
	} {
		if got := LyricsTag(c.tags); got != c.want {
			t.Errorf("LyricsTag(%v) = %q, want %q", c.tags, got, c.want)
		}
	}
}

func FuzzParseLyrics(f *testing.F) {
	for _, s := range []string{"[00:01.00]a", "[offset:x]", "[99:99.999][00:00]b", "<00:01>c", "plain\n\nlines"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		lines, synced := ParseLyrics(s)
		for i, l := range lines {
			if l.Start < 0 || (!synced && l.Start != 0) || (synced && i > 0 && l.Start < lines[i-1].Start) {
				t.Fatalf("ParseLyrics(%q): %+v", s, lines)
			}
		}
	})
}
