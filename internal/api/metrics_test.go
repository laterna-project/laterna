package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	laternav1 "github.com/laterna-project/laterna/internal/api/gen/laterna/v1"
	"github.com/laterna-project/laterna/internal/api/gen/laterna/v1/laternav1connect"
	"github.com/laterna-project/laterna/internal/app"
	"github.com/laterna-project/laterna/internal/store"
)

// Metrics: /metrics is not found as long as no token exists, a token is required, calls and
// requests are counted by procedure and by route, the token is renewed, metrics are turned off.
func TestMetrics(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	a, err := app.New(ctx, st, app.Options{ServerName: "Test", CacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(NewHandler(a, Options{}))
	t.Cleanup(srv.Close)
	setup, err := laternav1connect.NewAuthServiceClient(srv.Client(), srv.URL).Setup(ctx, connect.NewRequest(&laternav1.SetupRequest{
		Username: "admin", Password: "a-strong-password", Device: &laternav1.Device{Name: "Test", Client: "Test", ClientVersion: "1", Platform: "Go"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	admin := setup.Msg.GetToken()
	system := laternav1connect.NewSystemServiceClient(srv.Client(), srv.URL)
	scrape := func(token string) (int, string) {
		t.Helper()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/metrics", nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}

	if code, _ := scrape("x"); code != http.StatusNotFound {
		t.Errorf("metrics off: %d", code)
	}
	enabled, err := system.EnableMetrics(ctx, authed(&laternav1.EnableMetricsRequest{}, admin))
	if err != nil || !strings.HasPrefix(enabled.Msg.GetToken(), "lmt_") || enabled.Msg.GetPath() != "/metrics" {
		t.Fatalf("enable: %v %v", enabled, err)
	}
	token := enabled.Msg.GetToken()
	for _, bad := range []string{"", admin, token + "x"} {
		if code, _ := scrape(bad); code != http.StatusUnauthorized {
			t.Errorf("token %.8q...: %d", bad, code)
		}
	}
	if resp, err := http.Get(srv.URL + "/health"); err == nil {
		_ = resp.Body.Close()
	}
	code, body := scrape(token)
	if code != http.StatusOK {
		t.Fatalf("scrape: %d %s", code, body)
	}
	for _, want := range []string{
		`laterna_build_info{version="`,
		`laterna_rpc_requests_total{procedure="/laterna.v1.SystemService/EnableMetrics",code="ok"} 1`,
		`laterna_rpc_requests_total{procedure="/laterna.v1.AuthService/Setup",code="ok"} 1`,
		`laterna_rpc_duration_seconds_count{procedure="/laterna.v1.AuthService/Setup"} 1`,
		`laterna_http_requests_total{route="GET /health",status="2xx"} 1`,
		`laterna_http_requests_total{route="GET /metrics",status="4xx"}`,
		`laterna_activity_total{kind="settings.updated"} 1`,
		"laterna_database_bytes ",
		"laterna_event_streams 0",
		"process_resident_memory_bytes ",
		"# TYPE laterna_jobs gauge",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("%q missing", want)
		}
	}

	// A new token replaces the old one.
	again, err := system.EnableMetrics(ctx, authed(&laternav1.EnableMetricsRequest{}, admin))
	if err != nil || again.Msg.GetToken() == token {
		t.Fatalf("new token: %v %v", again, err)
	}
	if code, _ := scrape(token); code != http.StatusUnauthorized {
		t.Errorf("old token still valid: %d", code)
	}
	if code, _ := scrape(again.Msg.GetToken()); code != http.StatusOK {
		t.Errorf("new token refused: %d", code)
	}
	if _, err := system.DisableMetrics(ctx, authed(&laternav1.DisableMetricsRequest{}, admin)); err != nil {
		t.Fatal(err)
	}
	if code, _ := scrape(again.Msg.GetToken()); code != http.StatusNotFound {
		t.Errorf("metrics off: %d", code)
	}
	if st, err := system.GetMetrics(ctx, authed(&laternav1.GetMetricsRequest{}, admin)); err != nil || st.Msg.GetEnabled() {
		t.Errorf("state: %v %v", st, err)
	}
}
