package download

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestImage(t *testing.T) {
	var calls, busy atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("User-Agent") != "Laterna/test" {
			http.Error(w, "agent", http.StatusBadRequest)
			return
		}
		switch r.URL.Path {
		case "/affiche.jpg":
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write([]byte("jpeg"))
		case "/occupe.jpg":
			if busy.Add(1) == 1 {
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("png"))
		case "/page.jpg":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html>"))
		case "/enorme.jpg":
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write([]byte(strings.Repeat("x", MaxSize+1)))
		case "/panne.jpg":
			w.WriteHeader(http.StatusBadGateway)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := New("Laterna/test", time.Millisecond, srv.Client())
	ctx := context.Background()
	dir := t.TempDir()

	dst := filepath.Join(dir, "a", "b", "affiche.jpg")
	if err := c.Image(ctx, srv.URL+"/affiche.jpg", dst); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(dst); err != nil || string(b) != "jpeg" {
		t.Errorf("content: %q %v", b, err)
	}
	// A 429 makes us wait, then try again.
	if err := c.Image(ctx, srv.URL+"/occupe.jpg", filepath.Join(dir, "occupe.png")); err != nil {
		t.Errorf("after a 429: %v", err)
	}
	for _, name := range []string{"absente.jpg", "page.jpg", "enorme.jpg"} {
		err := c.Image(ctx, srv.URL+"/"+name, filepath.Join(dir, name))
		if !errors.Is(err, ErrUnavailable) {
			t.Errorf("%s: %v, want ErrUnavailable", name, err)
		}
		if _, statErr := os.Stat(filepath.Join(dir, name)); statErr == nil {
			t.Errorf("%s: file left behind", name)
		}
	}
	// A site outage will be retried later; a non-HTTP URL never.
	if err := c.Image(ctx, srv.URL+"/panne.jpg", filepath.Join(dir, "panne.jpg")); err == nil || errors.Is(err, ErrUnavailable) {
		t.Errorf("outage: %v", err)
	}
	before := calls.Load()
	if err := c.Image(ctx, "file:///etc/passwd", filepath.Join(dir, "x")); !errors.Is(err, ErrUnavailable) || calls.Load() != before {
		t.Errorf("local address: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".telechargement-") {
			t.Errorf("temporary file left behind: %s", e.Name())
		}
	}
}

func TestPacing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("x"))
	}))
	defer srv.Close()
	c := New("Laterna/test", 50*time.Millisecond, srv.Client())
	start := time.Now()
	for i := range 3 {
		if err := c.Image(context.Background(), srv.URL+"/x.jpg", filepath.Join(t.TempDir(), "x"+string(rune('0'+i)))); err != nil {
			t.Fatal(err)
		}
	}
	if d := time.Since(start); d < 100*time.Millisecond {
		t.Errorf("three requests in %v: no pacing", d)
	}
}
