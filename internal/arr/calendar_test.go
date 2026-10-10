package arr_test

import (
	"context"
	"testing"
	"time"

	"github.com/laterna-project/laterna/internal/arr"
	"github.com/laterna-project/laterna/internal/arr/arrtest"
)

func TestCalendar(t *testing.T) {
	ctx := context.Background()
	from := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 0, 14)
	poster := func(u string) []any { return []any{map[string]any{"coverType": "poster", "remoteUrl": u}} }

	sonarr := arrtest.New(t, arr.Sonarr)
	sonarr.Set(func(s *arrtest.Server) {
		s.Calendar = []map[string]any{
			{
				"title": "The Journey's End", "seasonNumber": 2, "episodeNumber": 5, "airDateUtc": "2026-03-12T15:30:00Z", "hasFile": false,
				"series": map[string]any{
					"title": "Frieren", "tvdbId": 101, "path": `D:\media\shows\Frieren`, "certification": "TV-14",
					"images": poster("https://artworks.example/frieren.jpg"),
				},
			},
			// Already there, without a series, without a date: the last two are not releases.
			{"title": "Aired", "seasonNumber": 2, "episodeNumber": 4, "airDateUtc": "2026-03-10T15:30:00Z", "hasFile": true, "series": map[string]any{"title": "Frieren", "tvdbId": 101, "rootFolderPath": "/data/media/shows/"}},
			{"title": "Orphan", "airDateUtc": "2026-03-12T15:30:00Z"},
			{"title": "Undated", "series": map[string]any{"title": "Frieren", "tvdbId": 101}},
		}
	})
	got, err := arr.New(arr.Sonarr, sonarr.URL, arrtest.Key, sonarr.Client()).Calendar(ctx, from, to)
	if err != nil || len(got) != 2 {
		t.Fatalf("Sonarr: %+v %v", got, err)
	}
	want := arr.Release{
		Title: "The Journey's End", Parent: "Frieren", ExternalID: "101", Season: 2, Episode: 5,
		At: time.Date(2026, 3, 12, 15, 30, 0, 0, time.UTC), RootFolder: "D:/media/shows", Certification: "TV-14",
		Poster: "https://artworks.example/frieren.jpg",
	}
	if got[0] != want {
		t.Errorf("episode:\n%+v\nwant\n%+v", got[0], want)
	}
	if !got[1].HasFile || got[1].RootFolder != "/data/media/shows" {
		t.Errorf("episode with a file: %+v", got[1])
	}

	radarr := arrtest.New(t, arr.Radarr)
	radarr.Set(func(s *arrtest.Server) {
		s.Calendar = []map[string]any{
			{
				"title": "Suzume", "tmdbId": 916224, "digitalRelease": "2026-03-20T00:00:00Z", "physicalRelease": "2026-04-02T00:00:00Z",
				"inCinemas": "2026-01-10T00:00:00Z", "rootFolderPath": "/data/media/movies", "certification": "PG", "images": poster("https://image.example/suzume.jpg"),
			},
			// Only out on disc, then only in theaters, then at home but outside the window.
			{"title": "On Disc", "tmdbId": 2, "physicalRelease": "2026-03-11T00:00:00Z", "path": "/data/media/movies/On Disc (2025)"},
			{"title": "In Theaters", "tmdbId": 3, "inCinemas": "2026-03-15T00:00:00Z"},
			{"title": "Later", "tmdbId": 4, "inCinemas": "2026-03-15T00:00:00Z", "digitalRelease": "2026-06-01T00:00:00Z"},
		}
	})
	got, err = arr.New(arr.Radarr, radarr.URL, arrtest.Key, radarr.Client()).Calendar(ctx, from, to)
	if err != nil || len(got) != 2 {
		t.Fatalf("Radarr: %+v %v", got, err)
	}
	want = arr.Release{
		Title: "Suzume", ExternalID: "916224", At: time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC), AllDay: true,
		RootFolder: "/data/media/movies", Certification: "PG", Poster: "https://image.example/suzume.jpg",
	}
	if got[0] != want {
		t.Errorf("movie:\n%+v\nwant\n%+v", got[0], want)
	}
	if got[1].Title != "On Disc" || got[1].At.Day() != 11 || got[1].RootFolder != "/data/media/movies" {
		t.Errorf("movie on disc: %+v", got[1])
	}

	lidarr := arrtest.New(t, arr.Lidarr)
	lidarr.Set(func(s *arrtest.Server) {
		s.Calendar = []map[string]any{
			{
				"title": "Discovery", "releaseDate": "2026-03-13T00:00:00Z", "statistics": map[string]any{"trackFileCount": 0, "trackCount": 14},
				"images": []any{map[string]any{"coverType": "cover", "remoteUrl": "https://covers.example/discovery.jpg"}},
				"artist": map[string]any{"artistName": "Daft Punk", "foreignArtistId": "056e4f3e", "path": "/data/media/music/Daft Punk"},
			},
			{
				"title": "Complete", "releaseDate": "2026-03-14T00:00:00Z", "statistics": map[string]any{"trackFileCount": 3, "trackCount": 3},
				"artist": map[string]any{"artistName": "Daft Punk", "foreignArtistId": "056e4f3e"},
			},
			{"title": "No Artist", "releaseDate": "2026-03-13T00:00:00Z"},
		}
	})
	got, err = arr.New(arr.Lidarr, lidarr.URL, arrtest.Key, lidarr.Client()).Calendar(ctx, from, to)
	if err != nil || len(got) != 2 {
		t.Fatalf("Lidarr: %+v %v", got, err)
	}
	want = arr.Release{
		Title: "Discovery", Parent: "Daft Punk", ExternalID: "056e4f3e", At: time.Date(2026, 3, 13, 0, 0, 0, 0, time.UTC), AllDay: true,
		RootFolder: "/data/media/music", Poster: "https://covers.example/discovery.jpg",
	}
	if got[0] != want {
		t.Errorf("album:\n%+v\nwant\n%+v", got[0], want)
	}
	if !got[1].HasFile {
		t.Errorf("album with all its tracks: %+v", got[1])
	}
}
