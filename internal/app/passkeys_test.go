package app

import (
	"context"
	"testing"

	"github.com/laterna-project/laterna/internal/auth/webauthn/webauthntest"
	"github.com/laterna-project/laterna/internal/domain"
)

// Passkeys: registration on a signed-in account, login without a username, removal. Not available
// without a public URL.
func TestPasskeys(t *testing.T) {
	a, _ := newTestApp(t)
	ctx := context.Background()
	_, admin := setupAdmin(t, a)

	if _, _, err := a.BeginPasskeyRegistration(ctx, admin); !isKind(err, domain.ErrPrecondition) || a.PasskeysAvailable() {
		t.Fatalf("without a public URL: %v", err)
	}
	public := "https://media.example.org"
	_, err := a.UpdateSettings(ctx, admin, SettingsChanges{PublicURL: &public})
	mustNil(t, err)

	phone := webauthntest.New("https://media.example.org")
	id, opts, err := a.BeginPasskeyRegistration(ctx, admin)
	mustNil(t, err)
	resp, err := phone.Create(opts)
	mustNil(t, err)
	pk, err := a.FinishPasskeyRegistration(ctx, admin, id, resp, " Chloé's iPhone ")
	mustNil(t, err)
	if pk.Name != "Chloé's iPhone" || string(pk.CredentialID) != string(phone.ID()) {
		t.Fatalf("passkey: %+v", pk)
	}
	// The same request only works once.
	if _, err := a.FinishPasskeyRegistration(ctx, admin, id, resp, ""); !isKind(err, domain.ErrInvalid) {
		t.Errorf("request reused: %v", err)
	}

	login := func(auth *webauthntest.Authenticator) (Login, error) {
		t.Helper()
		id, opts, err := a.BeginPasskeyLogin()
		mustNil(t, err)
		resp, err := auth.Get(opts)
		mustNil(t, err)
		return a.FinishPasskeyLogin(ctx, id, resp, dev("TV"), "10.0.0.2")
	}
	l, err := login(phone)
	mustNil(t, err)
	if l.Token == "" || l.Session.Account.ID != admin.Account.ID {
		t.Fatalf("login: %+v", l)
	}
	p, err := a.Authenticate(ctx, l.Token, "10.0.0.2")
	if err != nil || p.Account.ID != admin.Account.ID {
		t.Errorf("session opened by the passkey: %+v %v", p, err)
	}
	keys, err := a.Passkeys(ctx, admin)
	mustNil(t, err)
	if len(keys) != 1 || keys[0].LastUsedAt == nil {
		t.Errorf("passkeys of the account: %+v", keys)
	}

	// Page from another origin: refused.
	phishing := *phone
	phishing.Origin = "https://media-example.org"
	if _, err := login(&phishing); !isKind(err, domain.ErrUnauthenticated) {
		t.Errorf("other origin: %v", err)
	}

	mustNil(t, a.DeletePasskey(ctx, admin, pk.ID))
	if _, err := login(phone); !isKind(err, domain.ErrUnauthenticated) {
		t.Errorf("passkey removed: %v", err)
	}
	if err := a.DeletePasskey(ctx, admin, pk.ID); !isKind(err, domain.ErrNotFound) {
		t.Errorf("passkey already removed: %v", err)
	}
}
