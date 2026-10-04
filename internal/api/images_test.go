package api

import (
	"context"
	"image"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/laterna-project/laterna/internal/app"
	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/store"
	"github.com/laterna-project/laterna/internal/testfixtures"
)

// posterOf scans the fixture movies and returns the analyzed poster of the first one.
func posterOf(t *testing.T) (http.Handler, domain.Image) {
	t.Helper()
	testfixtures.Library(t)
	_, ffprobe, _ := testfixtures.FFmpeg()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	a, err := app.New(ctx, st, app.Options{ServerName: "Test", FFprobe: ffprobe, NoAutoScans: true, CacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	if err := a.Start(runCtx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); a.Wait(); _ = st.Close() })
	login, err := a.Setup(ctx, "admin", "a-strong-password", domain.Device{Name: "Test", Client: "Test", ClientVersion: "1", Platform: "Go"}, "")
	if err != nil {
		t.Fatal(err)
	}
	p, err := a.Authenticate(ctx, login.Token, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.CreateLibrary(ctx, "Films", domain.LibraryMovies, []string{filepath.Join(testfixtures.Root(), "Films")}, ""); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(90 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if counts, err := st.Read().CountJobs(ctx); err != nil || len(counts) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("job queue never empty")
		}
	}
	page, err := a.ListMovies(ctx, p, app.ListQuery{PageSize: 1})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("movies: %v", err)
	}
	for _, img := range page.Items[0].Images {
		if img.Kind == domain.ImagePoster {
			return NewHandler(a, Options{}), img
		}
	}
	t.Fatal("no poster")
	return nil, domain.Image{}
}

func TestImagesRoute(t *testing.T) {
	h, poster := posterOf(t)
	url := "/images/" + poster.ID.String() + "/" + poster.Hash
	get := func(target string, header ...string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		for i := 0; i+1 < len(header); i += 2 {
			req.Header.Set(header[i], header[i+1])
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	rec := get(url + "?w=160")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/jpeg" ||
		rec.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Fatalf("resized version: %d %v", rec.Code, rec.Header())
	}
	cfg, _, err := image.DecodeConfig(rec.Body)
	if err != nil || cfg.Width != 160 {
		t.Errorf("image served: %d px %v", cfg.Width, err)
	}
	if again := get(url+"?w=160", "If-None-Match", rec.Header().Get("ETag")); again.Code != http.StatusNotModified {
		t.Errorf("revalidation: %d", again.Code)
	}
	if orig := get(url); orig.Code != http.StatusOK || orig.Header().Get("ETag") == rec.Header().Get("ETag") {
		t.Errorf("original: %d %v", orig.Code, orig.Header())
	}

	for target, want := range map[string]int{
		"/images/" + poster.ID.String() + "/perime":  http.StatusNotFound, // the image changed since
		"/images/not-an-id/" + poster.Hash:           http.StatusNotFound,
		"/images/" + domain.NewID().String() + "/ab": http.StatusNotFound,
		url + "?w=abc": http.StatusBadRequest,
		url + "?w=-5":  http.StatusBadRequest,
	} {
		if rec := get(target); rec.Code != want {
			t.Errorf("%s: %d, want %d", target, rec.Code, want)
		}
	}
}
