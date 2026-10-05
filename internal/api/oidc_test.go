package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"connectrpc.com/connect"

	laternav1 "github.com/laterna-project/laterna/internal/api/gen/laterna/v1"
	"github.com/laterna-project/laterna/internal/api/gen/laterna/v1/laternav1connect"
	"github.com/laterna-project/laterna/internal/app"
	"github.com/laterna-project/laterna/internal/oidc/oidctest"
	"github.com/laterna-project/laterna/internal/store"
)

// OpenID Connect login end to end: the administrator sets the provider up; a device starts a login,
// the "browser" follows the authorization URL to the server's callback page and confirms the
// device; the device gets its session. The account is created on the first login and found by its
// identity afterwards. Also covered: refusal in the browser and at the provider, unknown account
// without account creation.
func TestOIDCLoginOverHTTP(t *testing.T) {
	ctx := context.Background()
	idp := oidctest.New("laterna", "client-secret")
	t.Cleanup(idp.Close)
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	a, err := app.New(ctx, st, app.Options{ServerName: "Living room"})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(NewHandler(a, Options{}))
	t.Cleanup(srv.Close)
	auth := laternav1connect.NewAuthServiceClient(srv.Client(), srv.URL)
	system := laternav1connect.NewSystemServiceClient(srv.Client(), srv.URL)
	server := laternav1connect.NewServerServiceClient(srv.Client(), srv.URL)
	dev := &laternav1.Device{Name: "Living room TV", Client: "Tests", ClientVersion: "1", Platform: "Go"}
	admin, err := auth.Setup(ctx, connect.NewRequest(&laternav1.SetupRequest{Username: "Chloé", Password: "a-strong-password", Device: dev}))
	if err != nil {
		t.Fatal(err)
	}
	token := admin.Msg.GetToken()

	if _, err := auth.StartOidcLogin(ctx, connect.NewRequest(&laternav1.StartOidcLoginRequest{Device: dev})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("without a provider: %v", err)
	}
	public := srv.URL
	if _, err := system.UpdateSettings(ctx, authed(&laternav1.UpdateSettingsRequest{PublicUrl: &public}, token)); err != nil {
		t.Fatal(err)
	}
	if _, err := system.SetOidcProvider(ctx, authed(&laternav1.SetOidcProviderRequest{Issuer: "http://127.0.0.1:1", ClientId: "laterna", ClientSecret: "x"}, token)); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("unreachable provider accepted: %v", err)
	}
	set, err := system.SetOidcProvider(ctx, authed(&laternav1.SetOidcProviderRequest{
		Issuer: idp.URL, ClientId: "laterna", ClientSecret: "client-secret", Name: "Authelia", AutoCreate: true,
	}, token))
	if err != nil || !set.Msg.GetProvider().GetHasSecret() || set.Msg.GetProvider().GetCallbackUrl() != srv.URL+"/auth/oidc/callback" {
		t.Fatalf("provider: %v %v", set, err)
	}
	info, err := server.GetServerInfo(ctx, connect.NewRequest(&laternav1.GetServerInfoRequest{}))
	if err != nil || info.Msg.GetOidcProvider() != "Authelia" {
		t.Fatalf("button: %v %v", info, err)
	}

	// login runs a whole login, with the confirmation page getting answer (approve, deny, or
	// nothing if it should not show up). It returns the final state and the last page seen.
	confirmField := regexp.MustCompile(`name="confirm" value="([^"]+)"`)
	login := func(answer string) (*laternav1.PollOidcLoginResponse, string) {
		t.Helper()
		start, err := auth.StartOidcLogin(ctx, connect.NewRequest(&laternav1.StartOidcLoginRequest{Device: dev}))
		if err != nil {
			t.Fatal(err)
		}
		poll := func() *laternav1.PollOidcLoginResponse {
			t.Helper()
			resp, err := auth.PollOidcLogin(ctx, connect.NewRequest(&laternav1.PollOidcLoginRequest{LoginId: start.Msg.GetLoginId()}))
			if err != nil {
				t.Fatal(err)
			}
			return resp.Msg
		}
		if s := poll().GetState(); s != laternav1.OidcLoginState_OIDC_LOGIN_STATE_PENDING {
			t.Fatalf("before the browser: %v", s)
		}
		page := readBody(http.Get(start.Msg.GetAuthorizationUrl())) // follows the provider's redirect
		m := confirmField.FindStringSubmatch(page)
		if (m == nil) != (answer == "") {
			t.Fatalf("callback page (answer %q):\n%s", answer, page)
		}
		if m == nil {
			return poll(), page
		}
		if s := poll().GetState(); s != laternav1.OidcLoginState_OIDC_LOGIN_STATE_PENDING {
			t.Fatalf("before confirmation: %v", s)
		}
		form := url.Values{"confirm": {m[1]}, "action": {answer}}
		page = readBody(http.PostForm(srv.URL+app.OIDCCallbackPath, form))
		if again := readBody(http.PostForm(srv.URL+app.OIDCCallbackPath, form)); !strings.Contains(again, "Unknown or expired sign-in request") {
			t.Errorf("confirmation replayed:\n%s", again)
		}
		return poll(), page
	}

	first, page := login("approve")
	if first.GetState() != laternav1.OidcLoginState_OIDC_LOGIN_STATE_APPROVED || first.GetToken() == "" ||
		first.GetSession().GetAccount().GetUsername() != "lea" || !strings.Contains(page, "Signed in: you can go back to the app.") {
		t.Fatalf("first login: %v\n%s", first, page)
	}
	// Found by her identity, even if the provider changes the display name.
	idp.Username = "lea-renamed"
	again, _ := login("approve")
	if again.GetState() != laternav1.OidcLoginState_OIDC_LOGIN_STATE_APPROVED || again.GetSession().GetAccount().GetId() != first.GetSession().GetAccount().GetId() {
		t.Errorf("second login: %v", again)
	}

	// Refusal in the browser: nothing is opened or created.
	idp.Subject, idp.Username = "new-subject", "zoe"
	if denied, page := login("deny"); denied.GetState() != laternav1.OidcLoginState_OIDC_LOGIN_STATE_DENIED || !strings.Contains(page, "Sign-in refused from the browser.") || denied.GetMessageText().GetKey() != "oidc.denied_in_browser" {
		t.Errorf("refusal in the browser: %v\n%s", denied, page)
	}
	if _, err := auth.Login(ctx, connect.NewRequest(&laternav1.LoginRequest{Username: "zoe", Password: "x", Device: dev})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("account created despite the refusal: %v", err)
	}
	idp.Subject, idp.Username = "user-42", "lea"

	// Refusal at the provider.
	idp.Refuse = true
	refused, page := login("")
	if refused.GetState() != laternav1.OidcLoginState_OIDC_LOGIN_STATE_DENIED || !strings.Contains(page, "access_denied") {
		t.Errorf("refusal: %v\n%s", refused, page)
	}
	idp.Refuse = false

	// Unknown user, without account creation.
	if _, err := system.SetOidcProvider(ctx, authed(&laternav1.SetOidcProviderRequest{Issuer: idp.URL, ClientId: "laterna", Name: "Authelia"}, token)); err != nil {
		t.Fatal(err) // secret kept
	}
	idp.Subject, idp.Username = "other-subject", "unknown"
	unknown, _ := login("")
	if unknown.GetState() != laternav1.OidcLoginState_OIDC_LOGIN_STATE_DENIED || unknown.GetMessageText().GetKey() != "error.oidc.no_account" || unknown.GetMessageText().GetParams()["username"] != "unknown" ||
		!strings.Contains(unknown.GetMessage(), "No Laterna account for \"unknown\"") {
		t.Errorf("unknown account: %v", unknown)
	}
	// A username that is already known is linked to its account.
	idp.Subject, idp.Username = "chloe-at-authelia", "Chloé"
	linked, _ := login("approve")
	if linked.GetState() != laternav1.OidcLoginState_OIDC_LOGIN_STATE_APPROVED || linked.GetSession().GetAccount().GetUsername() != "Chloé" {
		t.Errorf("existing account: %v", linked)
	}
}

// readBody reads the body of a response.
func readBody(resp *http.Response, err error) string {
	if err != nil {
		return err.Error()
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return string(body)
}
