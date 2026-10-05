package app

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/laterna-project/laterna/internal/arr"
	"github.com/laterna-project/laterna/internal/arr/arrtest"
	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/store"
)

// startApp starts an application with its background work, without FFmpeg or fixtures.
func startApp(t *testing.T) (*App, *clock) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(ctx, st, Options{ServerName: "Test", NoAutoScans: true, CacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	c := newClock()
	a.now = c.now
	a.arrPoll = 10 * time.Millisecond
	runCtx, cancel := context.WithCancel(ctx)
	mustNil(t, a.Start(runCtx))
	t.Cleanup(func() { cancel(); a.Wait(); _ = st.Close() })
	return a, c
}

// hookServer forwards the webhooks it receives to the application, like the /hooks/{kind} route.
func hookServer(t *testing.T, a *App) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, secret, _ := r.BasicAuth()
		body, _ := io.ReadAll(r.Body)
		if err := a.ArrWebhook(r.Context(), strings.TrimPrefix(r.URL.Path, "/hooks/"), secret, body); err != nil {
			http.Error(w, err.Error(), http.StatusUnauthorized)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func pendingJobs(t *testing.T, a *App, kind string) int {
	t.Helper()
	counts, err := a.store.Read().CountJobs(context.Background())
	mustNil(t, err)
	for _, c := range counts {
		if c.Kind == kind && c.State == "pending" {
			return c.N
		}
	}
	return 0
}

func TestSonarrIntegration(t *testing.T) {
	a, clk := startApp(t)
	_, admin := setupAdmin(t, a)
	ctx := context.Background()
	fake := arrtest.New(t, arr.Sonarr)
	a.http = fake.Client()

	// Series library on .../tv/Anime; Sonarr, in a container, sees /tv.
	tv := filepath.Join(t.TempDir(), "tv")
	for _, d := range []string{"Anime/Dr. STONE", "Anime/Lost"} {
		mustNil(t, os.MkdirAll(filepath.Join(tv, filepath.FromSlash(d)), 0o750))
	}
	writeText(t, filepath.Join(tv, "Anime", "Dr. STONE", "tvshow.nfo"), "<tvshow><title>Dr. STONE</title></tvshow>")
	lib, err := a.CreateLibrary(ctx, "Anime", domain.LibraryShows, []string{filepath.Join(tv, "Anime")}, "")
	mustNil(t, err)
	waitIdle(t, a)
	fake.Set(func(s *arrtest.Server) {
		s.Folders = []arr.Folder{
			{Title: "Lost", Path: "/tv/Anime/Lost", HasFiles: true},
			{Title: "Dr. STONE", Path: "/tv/Anime/Dr. STONE", HasFiles: true},
			{Title: "Elsewhere", Path: "/tv/Others/Elsewhere", HasFiles: true},
			{Title: "Upcoming", Path: "/tv/Anime/Upcoming"},
		}
	})

	// Address or key refused; instance of the other kind.
	for _, c := range []struct{ kind, url, key string }{
		{"sonarr", "ftp://x", arrtest.Key},
		{"sonarr", fake.URL, ""},
		{"sonarr", fake.URL, "wrong"},
		{"radarr", fake.URL, arrtest.Key},
		{"plex", fake.URL, arrtest.Key},
	} {
		if _, err := a.SetIntegration(ctx, admin, domain.IntegrationKind(c.kind), c.url, c.key); !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("%+v: %v", c, err)
		}
	}
	st, err := a.SetIntegration(ctx, admin, domain.IntegrationSonarr, fake.URL+"/", arrtest.Key)
	mustNil(t, err)
	if !st.Reachable || st.Version != "9.9.9" || st.KodiMetadata || len(st.MissingOptions) != 7 || st.Webhook {
		t.Errorf("initial state: %+v", st)
	}
	if st.Folders != 3 || st.Unmapped != 1 || st.WithoutNFO != 1 || !slices.Equal(st.WithoutNFOTitles, []string{"Lost"}) {
		t.Errorf("folders: %+v", st)
	}

	// Full setup: Kodi metadata, webhook (tried by Sonarr), refresh then scan.
	hooks := hookServer(t, a)
	scanned := func() time.Time {
		l, err := a.store.Read().Library(ctx, lib.ID)
		mustNil(t, err)
		return *l.LastScanAt
	}
	before := scanned()
	clk.advance(time.Minute)
	st, err = a.ConfigureIntegration(ctx, admin, domain.IntegrationSonarr, domain.IntegrationSetup{KodiMetadata: true, WebhookURL: hooks.URL + "/", Refresh: true})
	mustNil(t, err)
	if !st.KodiMetadata || len(st.MissingOptions) != 0 || !st.Webhook {
		t.Errorf("after setup: %+v", st)
	}
	waitIdle(t, a)
	fake.Get(func(s *arrtest.Server) {
		if s.Refreshes != 1 || s.Hook == nil || s.Hook.URL != hooks.URL+"/hooks/sonarr" || s.Hook.User != "laterna" {
			t.Errorf("Sonarr: %d refreshes, webhook %+v", s.Refreshes, s.Hook)
		}
	})
	if !scanned().After(before) {
		t.Error("no scan after the refresh")
	}

	// Import: one scan of the library, in 30 s. Events close together make a single one.
	for range 3 {
		mustNil(t, fake.Send(map[string]any{"eventType": "Download", "series": map[string]any{"path": "/tv/Anime/Lost"}}))
	}
	mustNil(t, fake.Send(map[string]any{"eventType": "Grab", "series": map[string]any{"path": "/tv/Anime/Lost"}}))
	if n := pendingJobs(t, a, jobScanLibrary); n != 1 {
		t.Errorf("%d scans waiting, want 1", n)
	}
	if err := a.ArrWebhook(ctx, "sonarr", "wrong-secret", []byte(`{"eventType":"Download"}`)); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Errorf("wrong secret: %v", err)
	}
	if err := a.ArrWebhook(ctx, "radarr", "", []byte(`{"eventType":"Download"}`)); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Errorf("Radarr not configured: %v", err)
	}

	// A new webhook replaces the old secret.
	var old string
	fake.Get(func(s *arrtest.Server) { old = s.Hook.Secret })
	_, err = a.ConfigureIntegration(ctx, admin, domain.IntegrationSonarr, domain.IntegrationSetup{WebhookURL: hooks.URL})
	mustNil(t, err)
	if err := a.ArrWebhook(ctx, "sonarr", old, []byte(`{"eventType":"Test"}`)); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Errorf("old secret: %v", err)
	}
	mustNil(t, fake.Send(map[string]any{"eventType": "Test"}))

	// Forgetting: the webhook goes away from Sonarr.
	mustNil(t, a.DeleteIntegration(ctx, admin, domain.IntegrationSonarr))
	fake.Get(func(s *arrtest.Server) {
		if s.Hook != nil {
			t.Error("webhook still in Sonarr")
		}
	})
	list, err := a.Integrations(ctx)
	mustNil(t, err)
	if len(list) != 2 || list[0].URL != "" || list[1].Kind != domain.IntegrationRadarr {
		t.Errorf("integrations: %+v", list)
	}
	if _, err := a.ConfigureIntegration(ctx, admin, domain.IntegrationSonarr, domain.IntegrationSetup{Refresh: true}); !errors.Is(err, domain.ErrPrecondition) {
		t.Errorf("setup without an integration: %v", err)
	}
}

func TestIntegrationUnreachable(t *testing.T) {
	a, _ := startApp(t)
	_, admin := setupAdmin(t, a)
	ctx := context.Background()
	fake := arrtest.New(t, arr.Radarr)
	a.http = fake.Client()
	_, err := a.SetIntegration(ctx, admin, domain.IntegrationRadarr, fake.URL, arrtest.Key)
	mustNil(t, err)
	fake.Close()
	list, err := a.Integrations(ctx)
	mustNil(t, err)
	if len(list) != 2 {
		t.Fatalf("%d integrations", len(list))
	}
	if r := list[1]; r.Reachable || r.Error.Key != "error.integration.unreachable" || r.Error.Params["name"] != "Radarr" {
		t.Errorf("Radarr stopped: %+v", r)
	}
}
