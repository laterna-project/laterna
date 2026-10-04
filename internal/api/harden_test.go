package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
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

// forwarded adds X-Forwarded-For to every request of a client.
type forwarded struct{ ip string }

func (f forwarded) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("X-Forwarded-For", f.ip)
	return http.DefaultTransport.RoundTrip(r)
}

func newHardenedServer(t *testing.T, trusted ...string) (*app.App, *httptest.Server) {
	t.Helper()
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
	var proxies []netip.Prefix
	for _, p := range trusted {
		proxies = append(proxies, netip.MustParsePrefix(p))
	}
	srv := httptest.NewServer(NewHandler(a, Options{TrustedProxies: proxies}))
	t.Cleanup(srv.Close)
	return a, srv
}

// Hardening: the client address is only read behind a trusted proxy, logins are throttled per
// address, request size is capped.
func TestHardening(t *testing.T) {
	ctx := context.Background()
	dev := &laternav1.Device{Name: "Test", Client: "Test", ClientVersion: "1", Platform: "Go"}
	lastLogin := func(a *app.App) string {
		t.Helper()
		page, err := a.Activity(ctx, app.ActivityQuery{PageSize: 1})
		if err != nil || len(page.Entries) == 0 {
			t.Fatalf("log: %v %v", page, err)
		}
		return page.Entries[0].Text.String()
	}

	// Behind the trusted proxy the client address comes from X-Forwarded-For.
	a, srv := newHardenedServer(t, "127.0.0.1/32", "::1/128")
	viaProxy := func(ip string) laternav1connect.AuthServiceClient {
		return laternav1connect.NewAuthServiceClient(&http.Client{Transport: forwarded{ip}}, srv.URL)
	}
	if _, err := viaProxy("198.51.100.7").Setup(ctx, connect.NewRequest(&laternav1.SetupRequest{Username: "admin", Password: "a-strong-password", Device: dev})); err != nil {
		t.Fatal(err)
	}
	if _, err := viaProxy("198.51.100.7").Login(ctx, connect.NewRequest(&laternav1.LoginRequest{Username: "admin", Password: "a-strong-password", Device: dev})); err != nil {
		t.Fatal(err)
	}
	if s := lastLogin(a); !strings.Contains(s, "198.51.100.7") {
		t.Errorf("address behind the proxy: %s", s)
	}

	// Throttling: 10 login attempts per address, then one every 6 s (password failures have their
	// own limit on top of that); another address goes through.
	attacker := viaProxy("203.0.113.66")
	var refused error
	for i := range 11 {
		_, err := attacker.StartDeviceLogin(ctx, connect.NewRequest(&laternav1.StartDeviceLoginRequest{Device: dev}))
		if connect.CodeOf(err) == connect.CodeResourceExhausted {
			refused = err
			if i < 10 {
				t.Fatalf("throttled at attempt %d: %v", i+1, err)
			}
		}
	}
	if d := errorDetail(refused); d.GetCode() != "request.rate_limited" || d.GetParams()["retry_after_seconds"] == "" {
		t.Errorf("eleventh attempt: %v", refused)
	}
	if _, err := viaProxy("198.51.100.8").Login(ctx, connect.NewRequest(&laternav1.LoginRequest{Username: "admin", Password: "a-strong-password", Device: dev})); err != nil {
		t.Errorf("other address throttled: %v", err)
	}

	// Request size: 4 MiB at most in general, 32 MiB for themes.
	direct := laternav1connect.NewAuthServiceClient(srv.Client(), srv.URL)
	_, err := direct.Login(ctx, connect.NewRequest(&laternav1.LoginRequest{Username: "admin", Password: strings.Repeat("x", 5<<20), Device: dev}))
	if connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Errorf("5 MiB request: %v", err)
	}
	login, err := direct.Login(ctx, connect.NewRequest(&laternav1.LoginRequest{Username: "admin", Password: "a-strong-password", Device: dev}))
	if err != nil {
		t.Fatal(err)
	}
	themes := laternav1connect.NewThemeServiceClient(srv.Client(), srv.URL)
	list, err := themes.ListThemes(ctx, authed(&laternav1.ListThemesRequest{}, login.Msg.GetToken()))
	if err != nil {
		t.Fatal(err)
	}
	_, err = themes.SetThemeImage(ctx, authed(&laternav1.SetThemeImageRequest{
		ThemeId: list.Msg.GetThemes()[0].GetId(), Kind: laternav1.ThemeImageKind_THEME_IMAGE_KIND_LOGO, Data: make([]byte, 9<<20),
	}, login.Msg.GetToken()))
	if connect.CodeOf(err) == connect.CodeResourceExhausted {
		t.Errorf("9 MiB image refused by the request size limit: %v", err)
	}

	// Without a trusted proxy, X-Forwarded-For is ignored.
	b, srv2 := newHardenedServer(t)
	spoofed := laternav1connect.NewAuthServiceClient(&http.Client{Transport: forwarded{"6.6.6.6"}, Timeout: 10 * time.Second}, srv2.URL)
	if _, err := spoofed.Setup(ctx, connect.NewRequest(&laternav1.SetupRequest{Username: "admin", Password: "a-strong-password", Device: dev})); err != nil {
		t.Fatal(err)
	}
	if _, err := spoofed.Login(ctx, connect.NewRequest(&laternav1.LoginRequest{Username: "admin", Password: "a-strong-password", Device: dev})); err != nil {
		t.Fatal(err)
	}
	if s := lastLogin(b); strings.Contains(s, "6.6.6.6") || !strings.Contains(s, "127.0.0.1") {
		t.Errorf("header from a stranger taken into account: %s", s)
	}
}
