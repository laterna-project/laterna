package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/laterna-project/laterna/internal/app"
	"github.com/laterna-project/laterna/internal/store"
)

// newWebHandler serves a small web client next to the API: an index.html, a hashed asset and a
// file at the root.
func newWebHandler(t *testing.T) http.Handler {
	t.Helper()
	dir := t.TempDir()
	for name, content := range map[string]string{
		"index.html":           `<!doctype html><div id="app"></div>`,
		"assets/index-abc1.js": "console.log(1)",
		"favicon.svg":          "<svg></svg>",
	} {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	a, err := app.New(ctx, st, app.Options{ServerName: "Living room"})
	if err != nil {
		t.Fatal(err)
	}
	return NewHandler(a, Options{CORSOrigins: []string{"*"}, WebDir: dir})
}

func TestWebClient(t *testing.T) {
	h := newWebHandler(t)
	const page = "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"
	cases := []struct {
		name, method, path, accept string
		status                     int
		body, cache                string
	}{
		{"page", http.MethodGet, "/", page, http.StatusOK, `<div id="app">`, "no-cache"},
		{"route of the app", http.MethodGet, "/movies/abc", page, http.StatusOK, `<div id="app">`, "no-cache"},
		{"device login page", http.MethodGet, "/device?code=BDWP-HQPK", page, http.StatusOK, `<div id="app">`, "no-cache"},
		{"asset", http.MethodGet, "/assets/index-abc1.js", "*/*", http.StatusOK, "console.log(1)", "public, max-age=31536000, immutable"},
		{"file at the root", http.MethodGet, "/favicon.svg", "*/*", http.StatusOK, "<svg>", "no-cache"},
		{"head", http.MethodHead, "/movies", page, http.StatusOK, "", "no-cache"},
		{"missing asset", http.MethodGet, "/assets/index-old.js", "*/*", http.StatusNotFound, "", ""},
		{"unknown API call", http.MethodPost, "/laterna.v1.NoSuchService/Call", "application/json", http.StatusNotFound, "", ""},
		{"byte route with a wrong path", http.MethodGet, "/images/abc", "image/avif,image/webp,*/*", http.StatusNotFound, "", ""},
		{"post to a page", http.MethodPost, "/movies", page, http.StatusNotFound, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(c.method, c.path, nil)
			req.Header.Set("Accept", c.accept)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != c.status {
				t.Fatalf("status %d, want %d (%s)", rec.Code, c.status, rec.Body.String())
			}
			if c.body != "" && !strings.Contains(rec.Body.String(), c.body) {
				t.Errorf("body %q, want %q", rec.Body.String(), c.body)
			}
			if c.cache != "" && rec.Header().Get("Cache-Control") != c.cache {
				t.Errorf("Cache-Control %q, want %q", rec.Header().Get("Cache-Control"), c.cache)
			}
			if c.status == http.StatusNotFound && strings.Contains(rec.Body.String(), `<div id="app">`) {
				t.Error("a 404 must not carry the page")
			}
		})
	}
}

// The server's own routes always win over the web client.
func TestWebClientBehindServerRoutes(t *testing.T) {
	h := newWebHandler(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status"`) {
		t.Fatalf("health: %d %s", rec.Code, rec.Body.String())
	}
	req := httptest.NewRequest(http.MethodPost, "/laterna.v1.ServerService/GetServerInfo", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Living room") {
		t.Fatalf("API: %d %s", rec.Code, rec.Body.String())
	}
}

// Without a web client, the root is not found, as before.
func TestNoWebClient(t *testing.T) {
	h, _ := newHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept", "text/html")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", rec.Code)
	}
}

// A path that climbs out of the folder (the router cleans it first; the handler does not rely on it)
// only ever reaches files inside.
func TestWebClientStaysInItsFolder(t *testing.T) {
	root := t.TempDir()
	web := filepath.Join(root, "web")
	if err := os.MkdirAll(web, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "secret.txt"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.URL.Path = "/../secret.txt"
	rec := httptest.NewRecorder()
	webClient(web).ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "secret") {
		t.Fatalf("status %d, body %q", rec.Code, rec.Body.String())
	}
}
