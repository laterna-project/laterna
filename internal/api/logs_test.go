package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"

	laternav1 "github.com/laterna-project/laterna/internal/api/gen/laterna/v1"
	"github.com/laterna-project/laterna/internal/api/gen/laterna/v1/laternav1connect"
	"github.com/laterna-project/laterna/internal/app"
	"github.com/laterna-project/laterna/internal/store"
)

// A log file can be downloaded with an administrator's token, and nothing else.
func TestLogFilesRoute(t *testing.T) {
	ctx := context.Background()
	logDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(logDir, "laterna_20260930.log"), []byte("time=... msg=hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	a, err := app.New(ctx, st, app.Options{ServerName: "Living room", LogDir: logDir})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(NewHandler(a, Options{}))
	t.Cleanup(srv.Close)
	auth := laternav1connect.NewAuthServiceClient(srv.Client(), srv.URL)
	device := &laternav1.Device{Name: "PC", Client: "Tests", ClientVersion: "1", Platform: "Go"}
	admin, err := auth.Setup(ctx, connect.NewRequest(&laternav1.SetupRequest{Username: "Chloé", Password: "a-strong-password", Device: device}))
	if err != nil {
		t.Fatal(err)
	}
	accounts := laternav1connect.NewAccountServiceClient(srv.Client(), srv.URL)
	req := connect.NewRequest(&laternav1.CreateAccountRequest{Username: "Léa", Password: "a-password"})
	req.Header().Set("Authorization", "Bearer "+admin.Msg.GetToken())
	if _, err := accounts.CreateAccount(ctx, req); err != nil {
		t.Fatal(err)
	}
	lea, err := auth.Login(ctx, connect.NewRequest(&laternav1.LoginRequest{Username: "Léa", Password: "a-password", Device: device}))
	if err != nil {
		t.Fatal(err)
	}

	get := func(name, token string) (int, string) {
		t.Helper()
		r, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/logs/"+name, nil)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := srv.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}
	if code, body := get("laterna_20260930.log", admin.Msg.GetToken()); code != http.StatusOK || body != "time=... msg=hello\n" {
		t.Errorf("administrator: %d %q", code, body)
	}
	for _, c := range []struct {
		name, token string
		want        int
	}{
		{"laterna_20260930.log", "", http.StatusUnauthorized},
		{"laterna_20260930.log", "lat_wrong", http.StatusUnauthorized},
		{"laterna_20260930.log", lea.Msg.GetToken(), http.StatusForbidden},
		{"laterna_20260101.log", admin.Msg.GetToken(), http.StatusNotFound},
		{"..%2Flaterna.db", admin.Msg.GetToken(), http.StatusNotFound},
	} {
		if code, _ := get(c.name, c.token); code != c.want {
			t.Errorf("%s (token %.8q): %d, want %d", c.name, c.token, code, c.want)
		}
	}
}
