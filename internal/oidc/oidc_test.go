package oidc_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/laterna-project/laterna/internal/oidc"
	"github.com/laterna-project/laterna/internal/oidc/oidctest"
)

// The whole code flow against the fake provider: discovery, authorization, exchange, verification.
// Then the tokens that must be rejected.
func TestCodeFlow(t *testing.T) {
	idp := oidctest.New("laterna", "secret")
	defer idp.Close()
	ctx := context.Background()
	p, err := oidc.Discover(ctx, http.DefaultClient, idp.URL+"/")
	if err != nil {
		t.Fatal(err)
	}
	state, nonce, verifier := oidc.Secret(), oidc.Secret(), oidc.Secret()
	redirect := "https://media.example.org/auth/oidc/callback"
	// The browser follows the authorization URL and the provider redirects to the callback.
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	authorize := func() string {
		t.Helper()
		resp, err := noFollow.Get(p.AuthCodeURL("laterna", redirect, state, nonce, verifier))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		back, err := url.Parse(resp.Header.Get("Location"))
		if err != nil || back.Query().Get("state") != state || back.Query().Get("code") == "" {
			t.Fatalf("provider redirect: %v %v", back, err)
		}
		return back.Query().Get("code")
	}
	if _, err := p.Exchange(ctx, "laterna", "secret", authorize(), "another-verifier", redirect); err == nil {
		t.Error("PKCE ignored")
	}
	token, err := p.Exchange(ctx, "laterna", "secret", authorize(), verifier, redirect)
	if err != nil {
		t.Fatal(err)
	}
	c, err := p.Verify(ctx, token, "laterna", nonce, time.Now())
	if err != nil || c.Subject != "user-42" || c.Username != "lea" || !c.EmailVerified || c.Name != "Léa" {
		t.Fatalf("token: %+v %v", c, err)
	}

	now := time.Now()
	claims := func(change func(map[string]any)) string {
		m := map[string]any{"iss": idp.URL, "sub": "s", "aud": "laterna", "exp": now.Add(time.Minute).Unix(), "iat": now.Unix(), "nonce": nonce}
		change(m)
		return idp.Sign(m)
	}
	for name, token := range map[string]string{
		"other nonce":       claims(func(m map[string]any) { m["nonce"] = "x" }),
		"other issuer":      claims(func(m map[string]any) { m["iss"] = "https://elsewhere" }),
		"other client":      claims(func(m map[string]any) { m["aud"] = "other" }),
		"expired":           claims(func(m map[string]any) { m["exp"] = now.Add(-time.Hour).Unix() }),
		"several audiences": claims(func(m map[string]any) { m["aud"] = []string{"laterna", "other"} }),
		"bad signature":     forged(claims(func(map[string]any) {}), claims(func(m map[string]any) { m["sub"] = "admin" })),
		"no signature":      "eyJhbGciOiJub25lIn0.eyJzdWIiOiJzIn0.",
	} {
		if _, err := p.Verify(ctx, token, "laterna", nonce, now); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// forged returns the token good with its payload replaced by that of other, signature kept.
func forged(good, other string) string {
	g, o := strings.Split(good, "."), strings.Split(other, ".")
	return g[0] + "." + o[1] + "." + g[2]
}

func TestDiscoverRefused(t *testing.T) {
	idp := oidctest.New("laterna", "secret")
	defer idp.Close()
	ctx := context.Background()
	if _, err := oidc.Discover(ctx, http.DefaultClient, "ftp://auth"); err == nil {
		t.Error("invalid address accepted")
	}
	// The provider announces itself under another address.
	if _, err := oidc.Discover(ctx, http.DefaultClient, strings.Replace(idp.URL, "127.0.0.1", "localhost", 1)); err == nil {
		t.Error("mismatched issuer accepted")
	}
}
