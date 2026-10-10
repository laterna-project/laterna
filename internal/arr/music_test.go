package arr_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/laterna-project/laterna/internal/arr"
	"github.com/laterna-project/laterna/internal/arr/arrtest"
)

var daftPunk = arrtest.MusicEntry{
	MBID: "056e4f3e", Name: "Daft Punk", Poster: "https://images.example/daft.jpg",
	Albums: []arrtest.AlbumEntry{
		{MBID: "rg-homework", Title: "Homework", Date: "1997-01-20", Cover: "https://images.example/homework.jpg"},
		{MBID: "rg-discovery", Title: "Discovery", Date: "2001-03-12"},
		{MBID: "rg-alive", Title: "Alive 2007", Type: "Live", Date: "2007-11-19"},
		{MBID: "rg-ram", Title: "Random Access Memories", Date: "2013-05-17"},
	},
}

// Lidarr in the integration: API v1, its Kodi options, webhook events and refresh command, artists
// as folders.
func TestLidarrIntegration(t *testing.T) {
	ctx := context.Background()
	srv := arrtest.New(t, arr.Lidarr)
	c := arr.New(arr.Lidarr, srv.URL, arrtest.Key, srv.Client())
	if st, err := c.Status(ctx); err != nil || st.AppName != "Lidarr" {
		t.Fatalf("identity: %+v %v", st, err)
	}
	if k, err := c.Kodi(ctx); err != nil || len(k.Missing) != 4 {
		t.Fatalf("Kodi: %+v %v", k, err)
	}
	if err := c.EnableKodi(ctx); err != nil {
		t.Fatal(err)
	}
	srv.Get(func(s *arrtest.Server) {
		if !s.KodiEnabled || !s.KodiOptions["albumImages"] || !s.KodiOptions["artistMetadata"] {
			t.Errorf("Kodi options: %v", s.KodiOptions)
		}
	})
	hook := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer hook.Close()
	if err := c.InstallWebhook(ctx, hook.URL+"/hooks/lidarr", "laterna", "secret"); err != nil {
		t.Fatal(err)
	}
	if w, ok, err := c.Webhook(ctx); err != nil || !ok || !w.Active {
		t.Errorf("webhook: %+v %v %v", w, ok, err)
	}
	if _, err := c.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	srv.Set(func(s *arrtest.Server) {
		if !slices.Contains(s.Commands, "RefreshArtist") {
			t.Errorf("commands: %v", s.Commands)
		}
		s.Folders = []arr.Folder{{Title: "Daft Punk", Path: "/data/media/music/Daft Punk", HasFiles: true}}
	})
	if f, err := c.Folders(ctx); err != nil || len(f) != 1 || f[0].Path != "/data/media/music/Daft Punk" || !f[0].HasFiles {
		t.Errorf("folders: %+v %v", f, err)
	}
	// A Sonarr client does not speak to Lidarr's API.
	if _, err := arr.New(arr.Sonarr, srv.URL, arrtest.Key, srv.Client()).Status(ctx); err == nil {
		t.Error("Lidarr answered on API v3")
	}
}

func TestMusicTitles(t *testing.T) {
	ctx := context.Background()
	srv := arrtest.New(t, arr.Lidarr)
	srv.Set(func(s *arrtest.Server) {
		s.Music = []arrtest.MusicEntry{daftPunk, {MBID: "justice", Name: "Justice", Albums: []arrtest.AlbumEntry{{MBID: "rg-cross", Title: "Cross", Date: "2007-06-11"}}}}
	})
	c := arr.New(arr.Lidarr, srv.URL, arrtest.Key, srv.Client())

	found, err := c.SearchMusic(ctx, "discovery")
	if err != nil || len(found) != 1 {
		t.Fatalf("search: %+v %v", found, err)
	}
	if f := found[0]; !f.Album || f.MBID != "rg-discovery" || f.Artist != "Daft Punk" || f.ArtistMBID != "056e4f3e" || f.Year != 2001 || f.ArrID != 0 {
		t.Errorf("album: %+v", f)
	}
	if found, _ := c.SearchMusic(ctx, "daft"); len(found) != 1 || found[0].Album || found[0].Poster != "https://images.example/daft.jpg" {
		t.Errorf("artist: %+v", found)
	}
	if mp, err := c.MetadataProfiles(ctx); err != nil || len(mp) != 2 {
		t.Errorf("metadata profiles: %+v %v", mp, err)
	}

	// An album alone: its artist is added without the other albums, the album is searched.
	opts := arr.MusicOptions{RootFolder: "/data/media/music", QualityProfileID: 7, MetadataProfileID: 1}
	albumID, artistID, err := c.AddAlbum(ctx, "rg-ram", opts)
	if err != nil || albumID == 0 || artistID == 0 {
		t.Fatalf("add album: %d %d %v", albumID, artistID, err)
	}
	srv.Get(func(s *arrtest.Server) {
		var monitored []string
		for _, al := range s.Albums {
			if al.Monitored {
				monitored = append(monitored, al.Title)
			}
		}
		if a := s.Artists[artistID]; a == nil || !a.Monitored || a.MonitorNewItems != "none" || !slices.Equal(monitored, []string{"Random Access Memories"}) {
			t.Errorf("artist %+v, monitored albums %v", a, monitored)
		}
	})
	if got, ok, err := c.TrackedMusic(ctx, true, "rg-ram"); err != nil || !ok || got.ArrID != albumID || got.Tracks != 10 {
		t.Errorf("tracked album: %+v %v %v", got, ok, err)
	}

	// The whole artist, now that Lidarr has it: every album monitored and searched.
	if id, err := c.AddArtist(ctx, "056e4f3e", opts); err != nil || id != artistID {
		t.Fatalf("add artist: %d %v", id, err)
	}
	srv.Get(func(s *arrtest.Server) {
		for _, al := range s.Albums {
			if !al.Monitored {
				t.Errorf("album left out: %s", al.Title)
			}
		}
		if s.Artists[artistID].MonitorNewItems != "all" || !slices.Contains(s.Commands, "AlbumSearch") {
			t.Errorf("artist %+v, commands %v", s.Artists[artistID], s.Commands)
		}
	})

	// A new artist for its latest album only (a live album does not count).
	justice, err := c.AddArtist(ctx, "justice", arr.MusicOptions{RootFolder: "/data/media/music", QualityProfileID: 7, MetadataProfileID: 1, Albums: "latest"})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := c.GetMusic(ctx, false, justice); err != nil || got.Title != "Justice" || !got.Monitored {
		t.Errorf("get artist: %+v %v", got, err)
	}
	if _, _, err := c.AddAlbum(ctx, "rg-unknown", opts); err == nil {
		t.Error("unknown album added")
	}

	// The queue is followed album by album.
	srv.Set(func(s *arrtest.Server) {
		s.Queue = []arr.Download{{ArrID: artistID, AlbumID: albumID, Size: 100, Left: 25}}
	})
	if q, err := c.Queue(ctx); err != nil || len(q) != 1 || q[0].AlbumID != albumID || q[0].ArrID != artistID || q[0].Left != 25 {
		t.Errorf("queue: %+v %v", q, err)
	}
}

func TestLidarrEvents(t *testing.T) {
	ev, err := arr.ParseEvent([]byte(`{"eventType":"Download","artist":{"path":"/data/media/music/Daft Punk"}}`))
	if err != nil || ev.Path != "/data/media/music/Daft Punk" || !ev.ChangesFiles() {
		t.Errorf("import: %+v %v", ev, err)
	}
	for _, kind := range []string{"Retag", "AlbumDelete", "ArtistDelete"} {
		if ev, _ := arr.ParseEvent([]byte(`{"eventType":"` + kind + `"}`)); !ev.ChangesFiles() {
			t.Errorf("%s: no scan", kind)
		}
	}
}
