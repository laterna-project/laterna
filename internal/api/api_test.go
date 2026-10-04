package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	laternav1 "github.com/laterna-project/laterna/internal/api/gen/laterna/v1"
	"github.com/laterna-project/laterna/internal/api/gen/laterna/v1/laternav1connect"
	"github.com/laterna-project/laterna/internal/app"
	"github.com/laterna-project/laterna/internal/store"
)

func newHandler(t *testing.T) (http.Handler, *store.Store) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	a, err := app.New(ctx, st, app.Options{ServerName: "Salon"})
	if err != nil {
		t.Fatal(err)
	}
	return NewHandler(a, Options{CORSOrigins: []string{"*"}}), st
}

func TestHealth(t *testing.T) {
	h, st := newHandler(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || body["status"] != "ok" {
		t.Fatalf("health: %d %v", rec.Code, body)
	}

	// Database closed: the probe must notice.
	_ = st.Close()
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("database unavailable: status %d, want 503", rec.Code)
	}
}

// A web client from another origin sends JSON and Connect headers: each call is preceded by a CORS
// preflight request, which must succeed, and the response must then expose Connect's error headers.
func TestConnectBehindCORS(t *testing.T) {
	h, _ := newHandler(t)
	const procedure = "/laterna.v1.ServerService/GetServerInfo"

	pre := httptest.NewRequest(http.MethodOptions, procedure, nil)
	pre.Header.Set("Origin", "http://localhost:5173")
	pre.Header.Set("Access-Control-Request-Method", "POST")
	pre.Header.Set("Access-Control-Request-Headers", "content-type,connect-protocol-version")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, pre)
	if rec.Code != http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Headers") != "content-type,connect-protocol-version" {
		t.Fatalf("preflight: %d %v", rec.Code, rec.Header())
	}

	req := httptest.NewRequest(http.MethodPost, procedure, strings.NewReader("{}"))
	req.Header.Set("Origin", "http://localhost:5173")
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("request: %d %v %s", rec.Code, rec.Header(), rec.Body.String())
	}
	if !strings.Contains(rec.Header().Get("Access-Control-Expose-Headers"), "Grpc-Status") {
		t.Errorf("Connect headers not exposed: %q", rec.Header().Get("Access-Control-Expose-Headers"))
	}
	if rec.Header().Get("X-Request-Id") == "" {
		t.Error("request ID missing")
	}
}

// A Connect stream goes through the middlewares: the access log must let Flush through.
func TestStreamingThroughMiddlewares(t *testing.T) {
	h, _ := newHandler(t)
	srv := httptest.NewServer(h)
	defer srv.Close()
	ctx := context.Background()
	setup, err := laternav1connect.NewAuthServiceClient(srv.Client(), srv.URL).Setup(ctx, connect.NewRequest(&laternav1.SetupRequest{
		Username: "admin", Password: "a-strong-password",
		Device: &laternav1.Device{Name: "Test", Client: "Test", ClientVersion: "1", Platform: "Go"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	req := connect.NewRequest(&laternav1.SubscribeRequest{})
	req.Header().Set("Authorization", "Bearer "+setup.Msg.GetToken())
	streamCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	stream, err := laternav1connect.NewEventServiceClient(srv.Client(), srv.URL).Subscribe(streamCtx, req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	if !stream.Receive() || stream.Msg().GetEvent().GetHeartbeat() == nil {
		t.Fatalf("stream: %v", stream.Err())
	}
}
