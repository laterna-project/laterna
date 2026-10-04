package arr_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/laterna-project/laterna/internal/arr"
	"github.com/laterna-project/laterna/internal/arr/arrtest"
)

func TestClient(t *testing.T) {
	ctx := context.Background()
	srv := arrtest.New(t, arr.Sonarr)
	c := arr.New(arr.Sonarr, srv.URL+"/", arrtest.Key, srv.Client())

	st, err := c.Status(ctx)
	if err != nil || st.Version != "9.9.9" {
		t.Fatalf("identity: %+v %v", st, err)
	}
	if _, err := arr.New(arr.Radarr, srv.URL, arrtest.Key, srv.Client()).Status(ctx); err == nil {
		t.Error("a Sonarr taken for a Radarr")
	}
	if _, err := arr.New(arr.Sonarr, srv.URL, "mauvaise", srv.Client()).Status(ctx); !errors.Is(err, arr.ErrUnauthorized) {
		t.Errorf("wrong key: %v", err)
	}

	// Kodi metadata: off, then on with the options we need. Other settings do not move.
	k, err := c.Kodi(ctx)
	if err != nil || k.Enabled || len(k.Missing) != 6 {
		t.Fatalf("Kodi: %+v %v", k, err)
	}
	if err := c.EnableKodi(ctx); err != nil {
		t.Fatal(err)
	}
	if k, err := c.Kodi(ctx); err != nil || !k.Enabled || len(k.Missing) != 0 {
		t.Errorf("after enabling: %+v %v", k, err)
	}
	srv.Get(func(s *arrtest.Server) {
		if s.KodiOptions["seriesMetadataEpisodeGuide"] {
			t.Error("unrelated option turned on")
		}
	})

	// Webhook: the instance tries it when saving.
	var got []string
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != "laterna" || p != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		got = append(got, r.URL.Path)
	}))
	defer hook.Close()
	if err := c.InstallWebhook(ctx, hook.URL+"/hooks/sonarr", "laterna", "mauvais"); err == nil {
		t.Error("webhook refused by Laterna but installed")
	} else if !errors.As(err, new(*arr.Error)) {
		t.Errorf("refusal: %v", err)
	}
	if _, ok, _ := c.Webhook(ctx); ok {
		t.Error("webhook saved although the test call failed")
	}
	if err := c.InstallWebhook(ctx, hook.URL+"/hooks/sonarr", "laterna", "secret"); err != nil {
		t.Fatal(err)
	}
	w, ok, err := c.Webhook(ctx)
	if err != nil || !ok || !w.Active || w.URL != hook.URL+"/hooks/sonarr" || !slices.Equal(got, []string{"/hooks/sonarr"}) {
		t.Errorf("webhook: %+v %v %v %v", w, ok, err, got)
	}
	if err := c.InstallWebhook(ctx, hook.URL+"/hooks/sonarr", "laterna", "secret"); err != nil { // update
		t.Fatal(err)
	}
	if err := c.RemoveWebhook(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := c.Webhook(ctx); ok {
		t.Error("webhook still there")
	}

	// Refresh: the command is followed until it ends.
	id, err := c.Refresh(ctx)
	if err != nil || id == 0 {
		t.Fatalf("refresh: %d %v", id, err)
	}
	if done, _, _ := c.Command(ctx, id); done {
		t.Error("command done on the first read")
	}
	if done, failed, err := c.Command(ctx, id); !done || failed != "" || err != nil {
		t.Errorf("command: %v %q %v", done, failed, err)
	}

	srv.Set(func(s *arrtest.Server) {
		s.Folders = []arr.Folder{{Title: "Dr. STONE", Path: "/tv/Animes/Dr. STONE", HasFiles: true}}
	})
	if f, err := c.Folders(ctx); err != nil || len(f) != 1 || f[0].Path != "/tv/Animes/Dr. STONE" || !f[0].HasFiles {
		t.Errorf("series: %+v %v", f, err)
	}
}

func TestParseEvent(t *testing.T) {
	e, err := arr.ParseEvent([]byte(`{"eventType":"Download","series":{"id":1,"path":"/tv/Animes/Dr. STONE"},"episodeFile":{}}`))
	if err != nil || e.Type != "Download" || e.Path != "/tv/Animes/Dr. STONE" || !e.ChangesFiles() {
		t.Errorf("Sonarr: %+v %v", e, err)
	}
	e, err = arr.ParseEvent([]byte(`{"eventType":"MovieDelete","movie":{"folderPath":"/movies/Suzume (2022)"}}`))
	if err != nil || e.Path != "/movies/Suzume (2022)" || !e.ChangesFiles() {
		t.Errorf("Radarr: %+v %v", e, err)
	}
	for _, body := range []string{`{"eventType":"Test"}`, `{"eventType":"Grab","series":{}}`} {
		if e, err := arr.ParseEvent([]byte(body)); err != nil || e.ChangesFiles() {
			t.Errorf("%s: %+v %v", body, e, err)
		}
	}
	for _, body := range []string{"not json", `{}`} {
		if _, err := arr.ParseEvent([]byte(body)); err == nil {
			t.Errorf("%s accepted", body)
		}
	}
}

func TestMapPath(t *testing.T) {
	base := t.TempDir()
	tv := filepath.Join(base, "data", "tv")
	for _, d := range []string{filepath.Join(tv, "Animes", "Dr. STONE"), filepath.Join(tv, "Séries", "Lost")} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	exists := func(p string) bool { st, err := os.Stat(p); return err == nil && st.IsDir() }
	for _, c := range []struct {
		arrPath string
		roots   []string
		want    string
	}{
		// Sonarr in a container (/tv <-> .../data/tv), library on the root or on a subfolder.
		{"/tv/Animes/Dr. STONE", []string{tv}, filepath.Join(tv, "Animes", "Dr. STONE")},
		{"/tv/Animes/Dr. STONE", []string{filepath.Join(tv, "Séries"), filepath.Join(tv, "Animes")}, filepath.Join(tv, "Animes", "Dr. STONE")},
		// Same machine, same paths.
		{filepath.Join(tv, "Séries", "Lost"), []string{tv}, filepath.Join(tv, "Séries", "Lost")},
		// Windows paths seen from elsewhere.
		{`D:\Médias\tv\Séries\Lost`, []string{tv}, filepath.Join(tv, "Séries", "Lost")},
		{"/tv/Animes/Inconnu", []string{tv}, ""},
		{"", []string{tv}, ""},
	} {
		got, ok := arr.MapPath(c.arrPath, c.roots, exists)
		if got != c.want || ok != (c.want != "") {
			t.Errorf("MapPath(%q, %v) = %q, %v; want %q", c.arrPath, c.roots, got, ok, c.want)
		}
	}
}
