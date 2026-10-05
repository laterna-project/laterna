package webauthn_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/laterna-project/laterna/internal/auth/webauthn"
	"github.com/laterna-project/laterna/internal/auth/webauthn/webauthntest"
)

var rp = webauthn.RP{ID: "media.example.com", Name: "Laterna", Origins: []string{"https://media.example.com"}}

func register(t *testing.T, a *webauthntest.Authenticator) webauthn.Credential {
	t.Helper()
	challenge := webauthn.NewChallenge()
	opts := webauthn.CreationOptions(rp, webauthn.User{Handle: []byte("account-1"), Name: "alice", DisplayName: "Alice"}, challenge, nil)
	resp, err := a.Create(opts)
	if err != nil {
		t.Fatal(err)
	}
	cred, err := webauthn.VerifyRegistration(rp, challenge, resp)
	if err != nil {
		t.Fatal(err)
	}
	return cred
}

func login(a *webauthntest.Authenticator, cred webauthn.Credential, challenge []byte) (uint32, error) {
	resp, err := a.Get(webauthn.RequestOptions(rp, challenge))
	if err != nil {
		return 0, err
	}
	return webauthn.VerifyAssertion(rp, challenge, cred, resp)
}

func TestRegisterAndLogin(t *testing.T) {
	a := webauthntest.New("https://media.example.com")
	cred := register(t, a)
	if string(cred.ID) != string(a.ID()) || len(cred.PublicKey) == 0 {
		t.Fatalf("passkey: %+v", cred)
	}
	// Synced passkey: the counter stays at zero.
	if n, err := login(a, cred, webauthn.NewChallenge()); err != nil || n != 0 {
		t.Fatalf("login: %d %v", n, err)
	}
	resp, err := a.Get(webauthn.RequestOptions(rp, webauthn.NewChallenge()))
	if err != nil {
		t.Fatal(err)
	}
	if id, handle, err := webauthn.CredentialID(resp); err != nil || string(id) != string(a.ID()) || string(handle) != "account-1" {
		t.Errorf("IDs: %q %q %v", id, handle, err)
	}
}

func TestRefused(t *testing.T) {
	a := webauthntest.New("https://media.example.com")
	cred := register(t, a)
	refused := func(name string, err error, want string) {
		t.Helper()
		if !errors.Is(err, webauthn.ErrInvalid) || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", name, err)
		}
	}

	// Response to another challenge (replay).
	resp, err := a.Get(webauthn.RequestOptions(rp, webauthn.NewChallenge()))
	if err != nil {
		t.Fatal(err)
	}
	_, err = webauthn.VerifyAssertion(rp, webauthn.NewChallenge(), cred, resp)
	refused("other challenge", err, "challenge")

	// Page from another origin (phishing).
	phishing := *a
	phishing.Origin = "https://media-example.com"
	_, err = login(&phishing, cred, webauthn.NewChallenge())
	refused("another origin", err, "origin")

	// User not verified.
	lazy := *a
	lazy.Verified = false
	_, err = login(&lazy, cred, webauthn.NewChallenge())
	refused("not verified", err, "verified")

	// Another key (bad signature).
	other := webauthntest.New("https://media.example.com")
	stolen := register(t, other)
	stolen.ID = cred.ID
	_, err = login(a, stolen, webauthn.NewChallenge())
	refused("other key", err, "signature")

	// Counter going backwards: cloned passkey.
	counted := webauthntest.New("https://media.example.com")
	counted.Count = 5
	c := register(t, counted)
	n, err := login(counted, c, webauthn.NewChallenge())
	if err != nil || n != 6 {
		t.Fatalf("counter: %d %v", n, err)
	}
	c.SignCount = 10
	_, err = login(counted, c, webauthn.NewChallenge())
	refused("counter", err, "counter")

	// Passkey of another site.
	elsewhere := webauthn.RP{ID: "other.example", Origins: rp.Origins}
	challenge := webauthn.NewChallenge()
	resp, err = a.Get(webauthn.RequestOptions(rp, challenge))
	if err != nil {
		t.Fatal(err)
	}
	_, err = webauthn.VerifyAssertion(elsewhere, challenge, cred, resp)
	refused("another site", err, "site")
}

func TestOptions(t *testing.T) {
	var opts map[string]any
	if err := json.Unmarshal(webauthn.CreationOptions(rp, webauthn.User{Handle: []byte{1, 2}, Name: "alice"}, []byte{3}, [][]byte{{4}}), &opts); err != nil {
		t.Fatal(err)
	}
	sel, _ := opts["authenticatorSelection"].(map[string]any)
	ex, _ := opts["excludeCredentials"].([]any)
	if opts["challenge"] != "Aw" || sel["residentKey"] != "required" || sel["userVerification"] != "required" || len(ex) != 1 || opts["attestation"] != "none" {
		t.Errorf("registration options: %v", opts)
	}
}

// Malformed responses must never panic.
func FuzzVerify(f *testing.F) {
	a := webauthntest.New("https://media.example.com")
	challenge := webauthn.NewChallenge()
	resp, _ := a.Create(webauthn.CreationOptions(rp, webauthn.User{Handle: []byte("x")}, challenge, nil))
	f.Add(resp)
	f.Add([]byte(`{"type":"public-key","rawId":"AA","response":{"attestationObject":"oA"}}`))
	f.Fuzz(func(_ *testing.T, data []byte) {
		_, _ = webauthn.VerifyRegistration(rp, challenge, data)
		_, _ = webauthn.VerifyAssertion(rp, challenge, webauthn.Credential{ID: []byte{0}}, data)
	})
}
