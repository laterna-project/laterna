package naming

import "testing"

func TestParseTrack(t *testing.T) {
	tests := []struct {
		rel  string
		want Track
	}{
		{"Artist Test/Album Test (2020)/01 - Track One.flac", Track{Title: "Track One", Number: 1, Album: "Album Test", Year: 2020, Artist: "Artist Test"}},
		{"Artist/Album/02. Two.mp3", Track{Title: "Two", Number: 2, Album: "Album", Artist: "Artist"}},
		{"Artist/Album/03 Three.mp3", Track{Title: "Three", Number: 3, Album: "Album", Artist: "Artist"}},
		// Lidarr or release style: artist and album first, then disc and number.
		{
			"Daft Punk - Discovery (2001) FLAC [16bit 44.1kHz]-CML34/Daft Punk - Discovery - 01-04 Harder, Better, Faster, Stronger.flac",
			Track{Title: "Harder, Better, Faster, Stronger", Disc: 1, Number: 4, Album: "Discovery", Year: 2001, Artist: "Daft Punk"},
		},
		// A dash in the title stays in the title.
		{"A/B/05 - Face - B.flac", Track{Title: "Face - B", Number: 5, Album: "B", Artist: "A"}},
		// Disc folder: the album is one level up.
		{"Artist/Album (1999)/CD2/03 Title.flac", Track{Title: "Title", Disc: 2, Number: 3, Album: "Album", Year: 1999, Artist: "Artist"}},
		{"Album/Disc 1/1 Intro.flac", Track{Title: "Intro", Disc: 1, Number: 1, Album: "Album"}},
		// No number, at the root: the whole name is the title.
		{"grand_project-wonders-of-the-earth-550792.mp3", Track{Title: "grand_project-wonders-of-the-earth-550792"}},
		{"CD1/01 X.flac", Track{Title: "X", Disc: 1, Number: 1}},
	}
	for _, tc := range tests {
		if got := ParseTrack(tc.rel); got != tc.want {
			t.Errorf("ParseTrack(%q)\n  = %+v\nwant %+v", tc.rel, got, tc.want)
		}
	}
}

func TestIsAudio(t *testing.T) {
	for p, want := range map[string]bool{"a.FLAC": true, "a.mp3": true, "a.m4a": true, "a.opus": true, "a.mkv": false, "a.jpg": false, "a": false} {
		if got := IsAudio(p); got != want {
			t.Errorf("IsAudio(%q) = %v", p, got)
		}
	}
}

func FuzzParseTrack(f *testing.F) {
	for _, s := range []string{"A/B (2020)/01 - C.flac", "A - B - 1-04 C.flac", "A/CD1/x.mp3", "99-999 x", "a/b/c/d/e.flac"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, p string) {
		// No input may make the parser panic, and numbers stay positive.
		if tr := ParseTrack(p); tr.Disc < 0 || tr.Number < 0 {
			t.Fatalf("inconsistent numbers for %q: %+v", p, tr)
		}
	})
}
