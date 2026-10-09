package arr_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/laterna-project/laterna/internal/arr"
	"github.com/laterna-project/laterna/internal/arr/arrtest"
)

func TestSeriesTitles(t *testing.T) {
	ctx := context.Background()
	srv := arrtest.New(t, arr.Sonarr)
	srv.Set(func(s *arrtest.Server) {
		s.Catalog = []arrtest.Entry{
			{ExternalID: 101, Title: "Frieren", Year: 2023, Seasons: []int{1, 2}, Poster: "https://artworks.example/frieren.jpg"},
			{ExternalID: 102, Title: "Mushishi", Year: 2005, Seasons: []int{1, 2, 3}},
			{ExternalID: 103, Title: "Monster", Year: 2004, Seasons: []int{1}},
		}
	})
	c := arr.New(arr.Sonarr, srv.URL, arrtest.Key, srv.Client())

	found, err := c.Search(ctx, "mushi")
	if err != nil || len(found) != 1 {
		t.Fatalf("search: %+v %v", found, err)
	}
	if f := found[0]; f.ExternalID != 102 || f.Title != "Mushishi" || f.Year != 2005 || !slices.Equal(f.Seasons, []int{1, 2, 3}) || f.ArrID != 0 {
		t.Errorf("result: %+v", f)
	}
	if f, ok, err := c.Find(ctx, 103); err != nil || !ok || f.Title != "Monster" {
		t.Errorf("find: %+v %v %v", f, ok, err)
	}
	if _, ok, err := c.Find(ctx, 999); err != nil || ok {
		t.Errorf("find unknown: %v %v", ok, err)
	}
	if roots, err := c.RootFolders(ctx); err != nil || len(roots) != 1 || roots[0].Path != "/data/media/shows" {
		t.Errorf("root folders: %+v %v", roots, err)
	}
	if profiles, err := c.QualityProfiles(ctx); err != nil || len(profiles) != 2 || profiles[1].ID != 7 {
		t.Errorf("quality profiles: %+v %v", profiles, err)
	}

	// The whole series: every season monitored and searched as Sonarr adds it.
	opts := arr.AddOptions{RootFolder: "/data/media/shows", QualityProfileID: 7, SeriesType: "anime", Seasons: "all"}
	id, err := c.Add(ctx, 101, opts)
	if err != nil {
		t.Fatal(err)
	}
	srv.Get(func(s *arrtest.Server) {
		tt := s.Titles[id]
		if tt == nil || !tt.Monitored || tt.Monitor != "all" || tt.SeriesType != "anime" || !slices.Equal(tt.MonitoredSeasons, []int{1, 2}) {
			t.Errorf("added: %+v", tt)
		}
	})
	got, ok, err := c.Tracked(ctx, 101)
	if err != nil || !ok || got.ArrID != id || got.Poster != "https://artworks.example/frieren.jpg" {
		t.Errorf("tracked: %+v %v %v", got, ok, err)
	}
	if _, ok, err := c.Tracked(ctx, 103); err != nil || ok {
		t.Errorf("not tracked: %v %v", ok, err)
	}

	// Chosen seasons: the series is monitored with those seasons only, then searched.
	id2, err := c.Add(ctx, 102, arr.AddOptions{RootFolder: "/data/media/shows", QualityProfileID: 7, Seasons: "chosen", SeasonNumbers: []int{2, 9}})
	if err != nil {
		t.Fatal(err)
	}
	srv.Get(func(s *arrtest.Server) {
		tt := s.Titles[id2]
		if !tt.Monitored || !slices.Equal(tt.MonitoredSeasons, []int{2}) {
			t.Errorf("chosen seasons: %+v", tt)
		}
		if !slices.Contains(s.Commands, "SeriesSearch") {
			t.Errorf("no search after monitoring: %v", s.Commands)
		}
	})

	// A title the instance already has is monitored again rather than added twice.
	if again, err := c.Add(ctx, 102, arr.AddOptions{RootFolder: "/data/media/shows", QualityProfileID: 7, Seasons: "latest"}); err != nil || again != id2 {
		t.Errorf("added twice: %d %v", again, err)
	}
	srv.Get(func(s *arrtest.Server) {
		if !slices.Equal(s.Titles[id2].MonitoredSeasons, []int{3}) || len(s.Titles) != 2 {
			t.Errorf("monitored again: %+v (%d titles)", s.Titles[id2], len(s.Titles))
		}
	})

	// The instance refuses a root folder it does not have, with its reason.
	_, err = c.Add(ctx, 103, arr.AddOptions{RootFolder: "/elsewhere", QualityProfileID: 7})
	var refused *arr.Error
	if !errors.As(err, &refused) || refused.Message != "Invalid root folder or quality profile" {
		t.Errorf("refusal: %v", err)
	}

	// The queue adds up the episodes of a series.
	srv.Set(func(s *arrtest.Server) {
		s.Queue = []arr.Download{{ArrID: id, Size: 100, Left: 40}, {ArrID: id, Size: 100, Left: 100}}
	})
	q, err := c.Queue(ctx)
	if err != nil || len(q) != 1 || q[0].ArrID != id || q[0].Size != 200 || q[0].Left != 140 {
		t.Errorf("queue: %+v %v", q, err)
	}
}

func TestMovieTitles(t *testing.T) {
	ctx := context.Background()
	srv := arrtest.New(t, arr.Radarr)
	srv.Set(func(s *arrtest.Server) {
		s.Catalog = []arrtest.Entry{{ExternalID: 550, Title: "Perfect Blue", Year: 1997, Poster: "https://images.example/perfect-blue.jpg"}}
	})
	c := arr.New(arr.Radarr, srv.URL, arrtest.Key, srv.Client())
	found, err := c.Search(ctx, "perfect")
	if err != nil || len(found) != 1 || found[0].ExternalID != 550 || found[0].Seasons != nil {
		t.Fatalf("search: %+v %v", found, err)
	}
	id, err := c.Add(ctx, 550, arr.AddOptions{RootFolder: "/data/media/movies", QualityProfileID: 7})
	if err != nil {
		t.Fatal(err)
	}
	srv.Set(func(s *arrtest.Server) {
		if tt := s.Titles[id]; tt == nil || !tt.Monitored || tt.Monitor != "movieOnly" {
			t.Errorf("added: %+v", tt)
		}
		s.Titles[id].HasFile = true
		s.Queue = []arr.Download{{ArrID: id, Size: 1000, Left: 250}}
	})
	if m, err := c.Get(ctx, id); err != nil || !m.HasFile || m.Title != "Perfect Blue" {
		t.Errorf("get: %+v %v", m, err)
	}
	if q, err := c.Queue(ctx); err != nil || len(q) != 1 || q[0].ArrID != id || q[0].Left != 250 {
		t.Errorf("queue: %+v %v", q, err)
	}
}
