package httpx

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func ok(w http.ResponseWriter, r *http.Request) {
	_, _ = io.WriteString(w, r.URL.Path)
}

func TestChainOrder(t *testing.T) {
	var order []string
	mw := func(name string) Middleware {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, name)
				next.ServeHTTP(w, r)
			})
		}
	}
	h := Chain(http.HandlerFunc(ok), mw("a"), mw("b"), mw("c"))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if strings.Join(order, "") != "abc" {
		t.Errorf("order %v, want a then b then c", order)
	}
}

func TestRecover(t *testing.T) {
	var logs bytes.Buffer
	h := Recover(slog.New(slog.NewTextHandler(&logs, nil)))(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("oups")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status %d", rec.Code)
	}
	if !strings.Contains(logs.String(), "oups") {
		t.Errorf("panic not logged: %s", logs.String())
	}
}

func TestRequestID(t *testing.T) {
	var seen string
	h := RequestID()(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = RequestIDFrom(r.Context())
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if seen == "" || rec.Header().Get(RequestIDHeader) != seen {
		t.Errorf("ID %q / header %q", seen, rec.Header().Get(RequestIDHeader))
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(RequestIDHeader, "amont-42")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if seen != "amont-42" {
		t.Errorf("the ID from an upstream proxy must be reused, got %q", seen)
	}
}

func TestAccessLogKeepsReaderFromAndStatus(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	h := AccessLog(log)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, ok := w.(io.ReaderFrom); !ok {
			t.Error("ReadFrom hidden: sendfile would be lost for files")
		}
		w.WriteHeader(http.StatusTeapot)
		_, _ = io.Copy(w, strings.NewReader("thé"))
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/theiere", nil))
	out := logs.String()
	if !strings.Contains(out, "status=418") || !strings.Contains(out, "bytes=4") || !strings.Contains(out, "level=INFO") {
		t.Errorf("unexpected log: %s", out)
	}
}

// Client gone before the response (a segment wait that was abandoned): 499, not 200.
func TestAccessLogClientGone(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	h := AccessLog(log)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/segment", nil).WithContext(ctx))
	if out := logs.String(); !strings.Contains(out, "status=499") || !strings.Contains(out, "level=INFO") {
		t.Errorf("unexpected log: %s", out)
	}
}

func TestCORSPreflight(t *testing.T) {
	h := CORS([]string{"*"})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("a preflight request must not reach the handler")
	}))
	req := httptest.NewRequest(http.MethodOptions, "/laterna.v1.ServerService/GetServerInfo", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	req.Header.Set("Access-Control-Request-Method", "GET")
	req.Header.Set("Access-Control-Request-Headers", "authorization")
	req.Header.Set("Access-Control-Request-Private-Network", "true")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status %d", rec.Code)
	}
	for header, want := range map[string]string{
		"Access-Control-Allow-Origin":          "*",
		"Access-Control-Allow-Headers":         "authorization",
		"Access-Control-Allow-Private-Network": "true",
	} {
		if got := rec.Header().Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
}

func TestCORSRestrictedOrigins(t *testing.T) {
	h := CORS([]string{"http://jellyweb.lan"})(http.HandlerFunc(ok))
	for origin, want := range map[string]string{"http://jellyweb.lan": "http://jellyweb.lan", "http://ailleurs.test": ""} {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.Header.Set("Origin", origin)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != want {
			t.Errorf("origin %s: Allow-Origin %q, want %q", origin, got, want)
		}
		if rec.Code != http.StatusOK {
			t.Errorf("origin %s: the request must go through (the browser blocks, not the server)", origin)
		}
	}
}

func TestWriteJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	if err := WriteJSON(rec, http.StatusCreated, map[string]string{"Nom": "Élodie"}); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusCreated || rec.Header().Get("Content-Type") != ContentTypeJSON {
		t.Errorf("status %d, type %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if rec.Body.String() != `{"Nom":"Élodie"}` {
		t.Errorf("body %q", rec.Body.String())
	}
	if err := WriteJSON(httptest.NewRecorder(), http.StatusOK, func() {}); err == nil {
		t.Error("a value that cannot be serialized must return an error")
	}
}
