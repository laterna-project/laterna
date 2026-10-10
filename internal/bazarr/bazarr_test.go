package bazarr_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/laterna-project/laterna/internal/bazarr"
	"github.com/laterna-project/laterna/internal/bazarr/bazarrtest"
)

func TestClient(t *testing.T) {
	ctx := context.Background()
	srv := bazarrtest.New(t)
	srv.Set(func(s *bazarrtest.Server) {
		s.Disabled = []bazarr.Language{{Code: "de", Name: "German"}}
		s.Series = []bazarr.Series{{ID: 7, TvdbID: 101, Title: "Frieren", Path: "/data/media/shows/Frieren"}}
		s.Episodes = []*bazarr.Video{
			{ID: 71, SeriesID: 7, Season: 1, Episode: 1, Title: "The Journey's End", Path: "/data/media/shows/Frieren/Season 01/Frieren S01E01.mkv"},
			{ID: 72, SeriesID: 7, Season: 1, Episode: 2, Path: "/data/media/shows/Frieren/Season 01/Frieren S01E02.mkv", Subtitles: []bazarr.Subtitle{{Language: "ja"}}},
			{ID: 81, SeriesID: 8, Season: 1, Episode: 1},
		}
		s.Movies = []*bazarr.Video{{ID: 3, ImdbID: "tt16428256", Title: "Suzume", Path: "/data/media/movies/Suzume (2022)/Suzume (2022).mkv"}}
		s.Find = func(v bazarr.Video, w bazarr.Wanted) string {
			if w.Language != "fr" {
				return ""
			}
			return v.Path[:len(v.Path)-len("mkv")] + "fr.srt"
		}
	})
	c := bazarr.New(srv.URL+"/", bazarrtest.Key, srv.Client())

	if v, err := c.Version(ctx); err != nil || v != "9.9.9" {
		t.Errorf("version: %q %v", v, err)
	}
	if _, err := bazarr.New(srv.URL, "wrong", srv.Client()).Version(ctx); !errors.Is(err, bazarr.ErrUnauthorized) {
		t.Errorf("wrong key: %v", err)
	}
	langs, err := c.Languages(ctx)
	if err != nil || !slices.Equal(langs, []bazarr.Language{{Code: "en", Name: "English"}, {Code: "fr", Name: "French"}}) {
		t.Errorf("languages: %+v %v", langs, err)
	}
	series, err := c.Series(ctx)
	if err != nil || len(series) != 1 || series[0] != (bazarr.Series{ID: 7, TvdbID: 101, Title: "Frieren", Path: "/data/media/shows/Frieren"}) {
		t.Errorf("series: %+v %v", series, err)
	}
	episodes, err := c.Episodes(ctx, 7)
	if err != nil || len(episodes) != 2 || episodes[0].ID != 71 || episodes[0].Season != 1 || episodes[0].Episode != 1 ||
		len(episodes[1].Subtitles) != 1 || episodes[1].Subtitles[0].Path != "" {
		t.Fatalf("episodes: %+v %v", episodes, err)
	}
	movies, err := c.Movies(ctx)
	if err != nil || len(movies) != 1 || movies[0].ImdbID != "tt16428256" {
		t.Fatalf("movies: %+v %v", movies, err)
	}

	// A search answers before anything is found: what arrived is read afterwards.
	mustNil := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	mustNil(c.SearchEpisode(ctx, 7, 71, bazarr.Wanted{Language: "de"}))
	if e, ok, err := c.Episode(ctx, 71); err != nil || !ok || len(e.Subtitles) != 0 {
		t.Errorf("nothing found: %+v %v %v", e, ok, err)
	}
	mustNil(c.SearchEpisode(ctx, 7, 71, bazarr.Wanted{Language: "fr", HearingImpaired: true}))
	e, ok, err := c.Episode(ctx, 71)
	want := bazarr.Subtitle{Language: "fr", HearingImpaired: true, Path: "/data/media/shows/Frieren/Season 01/Frieren S01E01.fr.srt"}
	if err != nil || !ok || len(e.Subtitles) != 1 || e.Subtitles[0] != want {
		t.Errorf("found: %+v %v %v", e, ok, err)
	}
	mustNil(c.SearchMovie(ctx, 3, bazarr.Wanted{Language: "fr", Forced: true}))
	if m, ok, err := c.Movie(ctx, 3); err != nil || !ok || len(m.Subtitles) != 1 || !m.Subtitles[0].Forced {
		t.Errorf("movie: %+v %v %v", m, ok, err)
	}
	if _, ok, err := c.Movie(ctx, 99); err != nil || ok {
		t.Errorf("unknown movie: %v %v", ok, err)
	}
	var got []bazarrtest.Search
	srv.Set(func(s *bazarrtest.Server) { got = slices.Clone(s.Searches) })
	if len(got) != 3 || got[1] != (bazarrtest.Search{SeriesID: 7, VideoID: 71, Wanted: bazarr.Wanted{Language: "fr", HearingImpaired: true}}) ||
		got[2].SeriesID != 0 || got[2].VideoID != 3 {
		t.Errorf("searches: %+v", got)
	}
}
