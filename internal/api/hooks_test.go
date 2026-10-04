package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	laternav1 "github.com/laterna-project/laterna/internal/api/gen/laterna/v1"
	"github.com/laterna-project/laterna/internal/api/gen/laterna/v1/laternav1connect"
	"github.com/laterna-project/laterna/internal/app"
	"github.com/laterna-project/laterna/internal/arr"
	"github.com/laterna-project/laterna/internal/arr/arrtest"
	"github.com/laterna-project/laterna/internal/store"
)

// Sonarr installs the webhook through the integration API, tries it on the real route, then calls
// it on each import.
func TestSonarrWebhookOverHTTP(t *testing.T) {
	ctx := context.Background()
	fake := arrtest.New(t, arr.Sonarr)
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	a, err := app.New(ctx, st, app.Options{ServerName: "Salon", HTTPClient: fake.Client()})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(NewHandler(a, Options{}))
	t.Cleanup(srv.Close)

	auth := laternav1connect.NewAuthServiceClient(srv.Client(), srv.URL)
	login, err := auth.Setup(ctx, connect.NewRequest(&laternav1.SetupRequest{
		Username: "Chloé", Password: "a-strong-password",
		Device: &laternav1.Device{Name: "PC", Client: "Tests", ClientVersion: "1", Platform: "Go"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	integrations := laternav1connect.NewIntegrationServiceClient(srv.Client(), srv.URL)
	call := func(msg any) error {
		t.Helper()
		var err error
		switch m := msg.(type) {
		case *laternav1.SetIntegrationRequest:
			req := connect.NewRequest(m)
			req.Header().Set("Authorization", "Bearer "+login.Msg.GetToken())
			_, err = integrations.SetIntegration(ctx, req)
		case *laternav1.ConfigureIntegrationRequest:
			req := connect.NewRequest(m)
			req.Header().Set("Authorization", "Bearer "+login.Msg.GetToken())
			var resp *connect.Response[laternav1.ConfigureIntegrationResponse]
			if resp, err = integrations.ConfigureIntegration(ctx, req); err == nil && !resp.Msg.GetIntegration().GetWebhook() {
				t.Errorf("webhook missing: %+v", resp.Msg.GetIntegration())
			}
		}
		return err
	}
	sonarr := laternav1.IntegrationKind_INTEGRATION_KIND_SONARR
	if err := call(&laternav1.SetIntegrationRequest{Kind: sonarr, Url: fake.URL, ApiKey: arrtest.Key}); err != nil {
		t.Fatal(err)
	}
	if err := call(&laternav1.ConfigureIntegrationRequest{Kind: sonarr, WebhookUrl: srv.URL}); err != nil {
		t.Fatal(err)
	}
	if err := fake.Send(map[string]any{"eventType": "Download", "series": map[string]any{"path": "/tv/X"}}); err != nil {
		t.Errorf("import: %v", err)
	}

	var secret string
	fake.Get(func(s *arrtest.Server) { secret = s.Hook.Secret })
	post := func(path, user, pass, body string) int {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		if user != "" {
			req.SetBasicAuth(user, pass)
		}
		rec := httptest.NewRecorder()
		NewHandler(a, Options{}).ServeHTTP(rec, req)
		return rec.Code
	}
	for _, c := range []struct {
		path, pass, body string
		want             int
	}{
		{"/hooks/sonarr", secret, `{"eventType":"Test"}`, http.StatusNoContent},
		{"/hooks/sonarr", "faux", `{"eventType":"Test"}`, http.StatusUnauthorized},
		{"/hooks/sonarr", "", `{"eventType":"Test"}`, http.StatusUnauthorized},
		{"/hooks/sonarr", secret, "not json", http.StatusBadRequest},
		{"/hooks/plex", secret, `{"eventType":"Test"}`, http.StatusNotFound},
		{"/hooks/sonarr", secret, strings.Repeat("x", maxHookBody+1), http.StatusRequestEntityTooLarge},
	} {
		if got := post(c.path, "laterna", c.pass, c.body); got != c.want {
			t.Errorf("%s (secret %q, %.20s): %d, want %d", c.path, c.pass, c.body, got, c.want)
		}
	}
}
