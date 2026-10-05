package api

import (
	"bytes"
	"context"
	"image"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	laternav1 "github.com/laterna-project/laterna/internal/api/gen/laterna/v1"
	"github.com/laterna-project/laterna/internal/api/gen/laterna/v1/laternav1connect"
	"github.com/laterna-project/laterna/internal/app"
	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/store"
	"github.com/laterna-project/laterna/internal/testfixtures"
)

// libraryServer starts a full server (contract and routes) whose given library (a fixture folder)
// is scanned and analyzed. It returns its address and an administrator token.
func libraryServer(t *testing.T, kind domain.LibraryKind, dir string) (string, string) {
	t.Helper()
	testfixtures.Library(t)
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	a, err := app.New(ctx, st, app.Options{ServerName: "Test", NoAutoScans: true, CacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	runCtx, stop := context.WithCancel(ctx)
	if err := a.Start(runCtx); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(NewHandler(a, Options{}))
	t.Cleanup(func() { srv.Close(); stop(); a.Wait(); _ = st.Close() })
	login, err := a.Setup(ctx, "admin", "a-strong-password", domain.Device{Name: "Test", Client: "Test", ClientVersion: "1", Platform: "Go"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.CreateLibrary(ctx, dir, kind, []string{filepath.Join(testfixtures.Root(), dir)}, ""); err != nil {
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
	return srv.URL, login.Token
}

// get makes a GET request (headers given as pairs). The body is closed when the test ends.
func get(t *testing.T, url string, header ...string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// Books over HTTP: BookService, resized or original pages, the whole file in ranges, a URL with the
// wrong key.
func TestBooksOverHTTP(t *testing.T) {
	base, token := libraryServer(t, domain.LibraryBooks, "Books")
	ctx := t.Context()
	books := laternav1connect.NewBookServiceClient(http.DefaultClient, base)

	series, err := books.ListBookSeries(ctx, authed(&laternav1.ListBookSeriesRequest{}, token))
	if err != nil || series.Msg.GetTotalSize() != 3 {
		t.Fatalf("series: %v %v", series, err)
	}
	manga := series.Msg.GetSeries()[1]
	got, err := books.GetBookSeries(ctx, authed(&laternav1.GetBookSeriesRequest{SeriesId: manga.GetId()}, token))
	if err != nil || len(got.Msg.GetBooks()) != 2 || got.Msg.GetBooks()[1].GetNumber() != 2 || len(manga.GetImages()) == 0 {
		t.Fatalf("series: %v %v", got, err)
	}
	volume := got.Msg.GetBooks()[0]
	full, err := books.GetBook(ctx, authed(&laternav1.GetBookRequest{BookId: volume.GetId()}, token))
	if err != nil || len(full.Msg.GetBook().GetCredits()) != 2 || len(full.Msg.GetFiles()) != 1 ||
		full.Msg.GetFiles()[0].GetBook().GetLayout() != laternav1.BookLayout_BOOK_LAYOUT_IMAGES {
		t.Fatalf("details: %v %v", full, err)
	}
	open, err := books.OpenBook(ctx, authed(&laternav1.OpenBookRequest{BookId: volume.GetId()}, token))
	if err != nil {
		t.Fatal(err)
	}
	o := open.Msg
	if !o.GetFile().GetRightToLeft() || len(o.GetPages()) != 3 || o.GetPages()[2].GetWidth() != 800 || o.GetPageUrlPrefix() == "" {
		t.Fatalf("open: %v", o)
	}

	// Resized page (next tier above 300: 320), no authentication, immutable caching.
	resp := get(t, base+o.GetPageUrlPrefix()+"2?w=300")
	body, _ := io.ReadAll(resp.Body)
	cfg, _, err := image.DecodeConfig(bytes.NewReader(body))
	if resp.StatusCode != http.StatusOK || err != nil || cfg.Width != 320 || !strings.Contains(resp.Header.Get("Cache-Control"), "immutable") {
		t.Fatalf("resized page: %d %+v %v", resp.StatusCode, cfg, err)
	}
	// Original page, and its ETag.
	resp = get(t, base+o.GetPageUrlPrefix()+"0")
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "image/png" || resp.Header.Get("ETag") == "" {
		t.Fatalf("original page: %d %v", resp.StatusCode, resp.Header)
	}
	if again := get(t, base+o.GetPageUrlPrefix()+"0", "If-None-Match", resp.Header.Get("ETag")); again.StatusCode != http.StatusNotModified {
		t.Errorf("page already cached: %d", again.StatusCode)
	}
	// Whole file in ranges.
	resp = get(t, base+o.GetFileUrl(), "Range", "bytes=0-3")
	body, _ = io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusPartialContent || string(body) != "PK\x03\x04" || resp.Header.Get("Content-Type") != "application/vnd.comicbook+zip" {
		t.Errorf("file: %d %q %v", resp.StatusCode, body, resp.Header)
	}
	// Wrong key, missing page, invalid width.
	wrong := strings.Replace(o.GetPageUrlPrefix(), "/pages/", "x/pages/", 1)
	for path, status := range map[string]int{
		wrong + "0": http.StatusNotFound, o.GetPageUrlPrefix() + "9": http.StatusNotFound, o.GetPageUrlPrefix() + "0?w=abc": http.StatusBadRequest,
	} {
		if resp := get(t, base+path); resp.StatusCode != status {
			t.Errorf("%s: %d, want %d", path, resp.StatusCode, status)
		}
	}

	// Progress.
	if _, err := books.SaveReadingProgress(ctx, authed(&laternav1.SaveReadingProgressRequest{BookId: volume.GetId(), Page: 1, Progression: 0.4}, token)); err != nil {
		t.Fatal(err)
	}
	open, err = books.OpenBook(ctx, authed(&laternav1.OpenBookRequest{BookId: volume.GetId()}, token))
	if err != nil || open.Msg.GetProgress().GetPage() != 1 || open.Msg.GetProgress().GetProgression() != 0.4 {
		t.Errorf("progress: %v %v", open, err)
	}
}
