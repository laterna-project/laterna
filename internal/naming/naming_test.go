package naming

import (
	"maps"
	"testing"
)

func TestIgnored(t *testing.T) {
	tests := map[string]bool{
		"Big Movie (2020)/Big Movie (2020).mkv":             false,
		"Big Movie (2020)/Extras/Making of.mkv":             true,
		"Big Movie (2020)/featurettes/Interview.mkv":        true,
		"Big Movie (2020)/big-movie-sample.mkv":             true,
		"Big Movie (2020)/sample.mkv":                       true,
		"Big Movie (2020)/Big Movie-trailer.mp4":            true,
		".cache/x.mkv":                                      true,
		"@eaDir/x.mkv/SYNOVIDEO_VIDEO_SCREENSHOT.jpg":       true,
		"Scenes From a Marriage (1973)/Scenes.mkv":          false,
		"Show/Season 1/Show S01E01 - Trailer Park Boys.mkv": false,
	}
	for p, want := range tests {
		if got := Ignored(p); got != want {
			t.Errorf("Ignored(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestParseMovie(t *testing.T) {
	tests := []struct {
		path string
		want Movie
	}{
		{
			"Big Test Movie (2020)/Big Test Movie (2020).mp4",
			Movie{Title: "Big Test Movie", Year: 2020, GroupKey: "Big Test Movie (2020)/bigtestmovie"},
		},
		{
			"Versions (2017)/Versions (2017) - 1080p.mp4",
			Movie{Title: "Versions", Year: 2017, Version: "1080p", GroupKey: "Versions (2017)/versions"},
		},
		{
			"Versions (2017)/Versions (2017) - 720p.mp4",
			Movie{Title: "Versions", Year: 2017, Version: "720p", GroupKey: "Versions (2017)/versions"},
		},
		{
			"Inception (2010).mkv",
			Movie{Title: "Inception", Year: 2010, GroupKey: "/inception-2010"},
		},
		{
			"Blade.Runner.2049.2017.1080p.BluRay.x264-GRP.mkv",
			Movie{Title: "Blade Runner 2049", Year: 2017, GroupKey: "/bladerunner2049-2017"},
		},
		{
			"The.Matrix.1999.MULTi.1080p.WEB.x264.mkv",
			Movie{Title: "The Matrix", Year: 1999, GroupKey: "/thematrix-1999"},
		},
		{
			"1917 (2019)/1917 (2019).mkv",
			Movie{Title: "1917", Year: 2019, GroupKey: "1917 (2019)/1917"},
		},
		{
			"2001 A Space Odyssey (1968)/2001.mkv",
			Movie{Title: "2001 A Space Odyssey", Year: 1968, GroupKey: "2001 A Space Odyssey (1968)/2001"},
		},
		{
			"Le Fabuleux Destin d'Amélie Poulain (2001) [tmdbid-194]/Amélie.mkv",
			Movie{Title: "Le Fabuleux Destin d'Amélie Poulain", Year: 2001, IDs: map[string]string{"tmdb": "194"}, GroupKey: "Le Fabuleux Destin d'Amélie Poulain (2001) [tmdbid-194]/amelie"},
		},
		{
			"Kill Bill (2003)/Kill Bill (2003) - cd2.mkv",
			Movie{Title: "Kill Bill", Year: 2003, Part: 2, GroupKey: "Kill Bill (2003)/killbill"},
		},
		{
			"Avatar (2009)/CD1/avatar.mkv",
			Movie{Title: "Avatar", Year: 2009, Part: 1, GroupKey: "Avatar (2009)/avatar"},
		},
		{
			"Mission Impossible - Fallout (2018).mkv",
			Movie{Title: "Mission Impossible - Fallout", Year: 2018, GroupKey: "/missionimpossiblefallout-2018"},
		},
		{
			"Movie Title (2019) - Director's Cut.mkv",
			Movie{Title: "Movie Title", Year: 2019, Version: "Director's Cut", GroupKey: "/movietitle-2019"},
		},
		{
			"Action Movies/Heat {imdb-tt0113277} (1995)/Heat.mkv",
			Movie{Title: "Heat", Year: 1995, IDs: map[string]string{"imdb": "tt0113277"}, GroupKey: "Action Movies/Heat {imdb-tt0113277} (1995)/heat"},
		},
	}
	for _, tt := range tests {
		got := ParseMovie(tt.path)
		if got.Title != tt.want.Title || got.Year != tt.want.Year || got.Version != tt.want.Version ||
			got.Part != tt.want.Part || got.GroupKey != tt.want.GroupKey || !maps.Equal(got.IDs, tt.want.IDs) {
			t.Errorf("ParseMovie(%q)\n  got  %+v\n  want %+v", tt.path, got, tt.want)
		}
	}
}

func TestParseEpisode(t *testing.T) {
	tests := []struct {
		path string
		want Episode
	}{
		{
			"Café Stories (2022)/Saison 01/Café Stories (2022) S01E02.mkv",
			Episode{SeriesDir: "Café Stories (2022)", SeriesTitle: "Café Stories", SeriesYear: 2022, Season: 1, Episode: 2},
		},
		{
			"Show/Season 2/Show - S02E05E06 - Double.mkv",
			Episode{SeriesDir: "Show", SeriesTitle: "Show", Season: 2, Episode: 5, EpisodeEnd: 6, Title: "Double"},
		},
		{
			"Show/S01E01-E02.mkv",
			Episode{SeriesDir: "Show", SeriesTitle: "Show", Season: 1, Episode: 1, EpisodeEnd: 2},
		},
		{
			"Show/Show S01E01-02.mkv",
			Episode{SeriesDir: "Show", SeriesTitle: "Show", Season: 1, Episode: 1, EpisodeEnd: 2},
		},
		{
			"Show/Show S01E01 720p.mkv",
			Episode{SeriesDir: "Show", SeriesTitle: "Show", Season: 1, Episode: 1},
		},
		{
			"Breaking.Bad.S05E14.Ozymandias.1080p.WEB.mkv",
			Episode{SeriesTitle: "Breaking Bad", Season: 5, Episode: 14, Title: "Ozymandias"},
		},
		{
			"Game of Thrones/Season 01/Game.of.Thrones.S01E01.Winter.Is.Coming.mkv",
			Episode{SeriesDir: "Game of Thrones", SeriesTitle: "Game of Thrones", Season: 1, Episode: 1, Title: "Winter Is Coming"},
		},
		{
			"Friends/Season 1/Friends 1x03 - The One with the Thumb.avi",
			Episode{SeriesDir: "Friends", SeriesTitle: "Friends", Season: 1, Episode: 3, Title: "The One with the Thumb"},
		},
		{
			"Anime Test/[Group] Anime Test - 01 [1080p].mkv",
			Episode{SeriesDir: "Anime Test", SeriesTitle: "Anime Test", Season: 1, Episode: 1, Absolute: true, AbsoluteNumber: 1},
		},
		{
			"Frieren/[SubsPlease] Frieren - 12 (1080p) [ABCD1234].mkv",
			Episode{SeriesDir: "Frieren", SeriesTitle: "Frieren", Season: 1, Episode: 12, Absolute: true, AbsoluteNumber: 12},
		},
		{
			// Seen in the wild (Sonarr): absolute number in parentheses after SxxEyy.
			"Bleach/Season 3/Bleach - S03E13 (054).mkv",
			Episode{SeriesDir: "Bleach", SeriesTitle: "Bleach", Season: 3, Episode: 13, AbsoluteNumber: 54},
		},
		{
			"Slime/Season 4/Slime - S04E02 - The Dungeon Evolves WEBDL-1080p.mkv",
			Episode{SeriesDir: "Slime", SeriesTitle: "Slime", Season: 4, Episode: 2, Title: "The Dungeon Evolves"},
		},
		{
			// Seen in the wild: group glued to the name, number followed by the episode title.
			"[Triggerforce] Naruto Shippuden Yabaï/Saison 1/[Triggerforce]Naruto Shippuden Yabaï 02 - Le réceptacle.mkv",
			Episode{SeriesDir: "[Triggerforce] Naruto Shippuden Yabaï", SeriesTitle: "Naruto Shippuden Yabaï", Season: 1, Episode: 2, Title: "Le réceptacle"},
		},
		{
			// The dash followed by a number wins: "100" is part of the name.
			"Mob Psycho 100/Mob Psycho 100 - 03 - The Title.mkv",
			Episode{SeriesDir: "Mob Psycho 100", SeriesTitle: "Mob Psycho 100", Season: 1, Episode: 3, Absolute: true, AbsoluteNumber: 3, Title: "The Title"},
		},
		{
			"One Piece/One Piece - 1071.mkv",
			Episode{SeriesDir: "One Piece", SeriesTitle: "One Piece", Season: 1, Episode: 1071, Absolute: true, AbsoluteNumber: 1071},
		},
		{
			"Show/Season 1/Show - Episode 5.mkv",
			Episode{SeriesDir: "Show", SeriesTitle: "Show", Season: 1, Episode: 5},
		},
		{
			"Show (2019) [tvdbid-123]/Specials/Show S00E01.mkv",
			Episode{SeriesDir: "Show (2019) [tvdbid-123]", SeriesTitle: "Show", SeriesYear: 2019, SeriesIDs: map[string]string{"tvdb": "123"}, Season: 0, Episode: 1},
		},
		{
			"Show/Season 3/03.mkv",
			Episode{SeriesDir: "Show", SeriesTitle: "Show", Season: 3, Episode: 3},
		},
		{
			"Mushishi/Season 1/07 - The Birds.mkv",
			Episode{SeriesDir: "Mushishi", SeriesTitle: "Mushishi", Season: 1, Episode: 7, Title: "The Birds"},
		},
	}
	for _, tt := range tests {
		got, ok := ParseEpisode(tt.path)
		if !ok {
			t.Errorf("ParseEpisode(%q): not recognized", tt.path)
			continue
		}
		if got.SeriesDir != tt.want.SeriesDir || got.SeriesTitle != tt.want.SeriesTitle || got.SeriesYear != tt.want.SeriesYear ||
			got.Season != tt.want.Season || got.Episode != tt.want.Episode || got.EpisodeEnd != tt.want.EpisodeEnd ||
			got.Absolute != tt.want.Absolute || got.AbsoluteNumber != tt.want.AbsoluteNumber || got.Title != tt.want.Title || !maps.Equal(got.SeriesIDs, tt.want.SeriesIDs) {
			t.Errorf("ParseEpisode(%q)\n  got  %+v\n  want %+v", tt.path, got, tt.want)
		}
	}
}

func TestParseEpisodeRejects(t *testing.T) {
	for _, p := range []string{
		"Show/Show 2019.mkv",
		"Show/Bonus 1080p.mkv",
		"Show/Making of.mkv",
	} {
		if got, ok := ParseEpisode(p); ok {
			t.Errorf("ParseEpisode(%q) wrongly recognized: %+v", p, got)
		}
	}
}

func TestSortTitle(t *testing.T) {
	for in, want := range map[string]string{
		"The Matrix":      "matrix",
		"Le Parrain":      "parrain",
		"L'Été meurtrier": "ete meurtrier",
		"A":               "a",
		"Élodie":          "elodie",
		"Les":             "les",
	} {
		if got := SortTitle(in); got != want {
			t.Errorf("SortTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsVideo(t *testing.T) {
	for p, want := range map[string]bool{"a.MKV": true, "a.mp4": true, "a.srt": false, "a.iso": false, "a": false} {
		if got := IsVideo(p); got != want {
			t.Errorf("IsVideo(%q) = %v", p, got)
		}
	}
}

func FuzzParse(f *testing.F) {
	for _, s := range []string{"Show/Season 1/Show S01E02.mkv", "[G] A - 01 [1080p].mkv", "Movie (2020)/CD1/x.mkv", "a/b/c/d.mkv", "S99E9999-E1.mkv"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, p string) {
		// No input may make the parser panic, and a recognized episode must be consistent.
		_ = ParseMovie(p)
		_ = Ignored(p)
		_ = SortTitle(p)
		if e, ok := ParseEpisode(p); ok {
			if e.Episode < 0 || (e.EpisodeEnd != 0 && e.EpisodeEnd <= e.Episode) {
				t.Fatalf("inconsistent episode for %q: %+v", p, e)
			}
		}
	})
}
