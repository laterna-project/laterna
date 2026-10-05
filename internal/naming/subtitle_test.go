package naming

import "testing"

func TestSidecarFor(t *testing.T) {
	const movie = "Movies/The Movie (2020)/The Movie (2020).mkv"
	cases := []struct {
		video, sub string
		alone      bool
		ok         bool
		want       Sidecar
	}{
		{movie, "Movies/The Movie (2020)/The Movie (2020).srt", true, true, Sidecar{}},
		{movie, "Movies/The Movie (2020)/The Movie (2020).fr.srt", true, true, Sidecar{Language: "fre"}},
		{movie, "Movies/The Movie (2020)/the movie (2020).FRE.forced.ass", true, true, Sidecar{Language: "fre", Forced: true}},
		{movie, "Movies/The Movie (2020)/The Movie (2020).en.sdh.srt", true, true, Sidecar{Language: "eng", HearingImpaired: true}},
		{movie, "Movies/The Movie (2020)/The Movie (2020).English.Commentary.default.vtt", true, true, Sidecar{Language: "eng", Title: "Commentary", Default: true}},
		{movie, "Movies/The Movie (2020)/The Movie (2020).pt-BR.srt", true, true, Sidecar{Language: "por"}},
		{movie, "Movies/The Movie (2020)/The Movie (2020).hi.srt", true, true, Sidecar{HearingImpaired: true}},
		{movie, "Movies/The Movie (2020)/The Movie (2020).sup", true, true, Sidecar{}},
		{movie, "Movies/The Movie (2020)/Subs/2_English.srt", true, true, Sidecar{Language: "eng"}},
		{movie, "Movies/The Movie (2020)/Subtitles/French Forced.srt", true, true, Sidecar{Language: "fre", Forced: true}},
		// Several videos in the folder: "Subs" does not say which one it belongs to.
		{movie, "Movies/The Movie (2020)/Subs/2_English.srt", false, false, Sidecar{}},
		{"Series/Show/Show.S01E01.mkv", "Series/Show/Subs/Show.S01E01/3_French.srt", false, true, Sidecar{Language: "fre"}},
		{"Series/Show/Show.S01E01.mkv", "Series/Show/Subs/Show.S01E02/3_French.srt", false, false, Sidecar{}},
		// Not this video's.
		{"Series/Show/Episode 1.mkv", "Series/Show/Episode 10.srt", false, false, Sidecar{}},
		{movie, "Movies/Other (2021)/The Movie (2020).srt", true, false, Sidecar{}},
		{movie, "Movies/The Movie (2020)/The Movie (2020).nfo", true, false, Sidecar{}},
		{movie, "Movies/The Movie (2020)/The Movie (2020) - excerpt.srt", true, false, Sidecar{}},
	}
	for _, c := range cases {
		got, ok := SidecarFor(c.video, c.sub, c.alone)
		if ok != c.ok || got != c.want {
			t.Errorf("SidecarFor(%q, %q, %v) = %+v %v, want %+v %v", c.video, c.sub, c.alone, got, ok, c.want, c.ok)
		}
	}
}

func TestIsSubtitle(t *testing.T) {
	for p, want := range map[string]bool{"a.SRT": true, "a.ass": true, "a.sup": true, "a.idx": true, "a.mkv": false, "a.txt": false} {
		if IsSubtitle(p) != want {
			t.Errorf("IsSubtitle(%q) = %v", p, !want)
		}
	}
}

func FuzzSidecarFor(f *testing.F) {
	f.Add("A/B.mkv", "A/B.fr.forced.srt", true)
	f.Add("A/B.mkv", "A/Subs/B/2_English.srt", false)
	f.Fuzz(func(t *testing.T, video, sub string, alone bool) {
		s, ok := SidecarFor(video, sub, alone)
		if !ok && s != (Sidecar{}) {
			t.Errorf("description without a match: %+v", s)
		}
	})
}
