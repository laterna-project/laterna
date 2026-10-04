package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/laterna-project/laterna/internal/auth/webauthn"
	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/store"
)

// Passkeys: signing in without a password, with a WebAuthn key from a phone, a computer or a
// security key. The server gives options (a random challenge, kept in memory for five minutes and
// usable once), the client passes them to its OS API and sends back the authenticator's response,
// which the server verifies.

const (
	maxPasskeys        = 20
	maxPasskeyNameLen  = 64
	maxPendingPasskeys = 10000
)

// passkeyChallenges keeps the challenges in progress; account is nil for a login.
type passkeyChallenges struct {
	mu   sync.Mutex
	byID map[string]passkeyChallenge
}

type passkeyChallenge struct {
	challenge []byte
	account   *domain.ID
	expires   time.Time
}

// put stores a challenge and returns its ID (opaque, random).
func (c *passkeyChallenges) put(ch passkeyChallenge, now time.Time) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.byID == nil {
		c.byID = map[string]passkeyChallenge{}
	}
	for id, old := range c.byID {
		if now.After(old.expires) {
			delete(c.byID, id)
		}
	}
	if len(c.byID) >= maxPendingPasskeys {
		return "", domain.TooManyAttempts("passkey.too_many_pending")
	}
	raw := make([]byte, 24)
	_, _ = rand.Read(raw)
	id := base64.RawURLEncoding.EncodeToString(raw)
	c.byID[id] = ch
	return id, nil
}

// take takes a challenge back (only once); false if it is unknown or expired.
func (c *passkeyChallenges) take(id string, now time.Time) (passkeyChallenge, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch, ok := c.byID[id]
	delete(c.byID, id)
	return ch, ok && !now.After(ch.expires)
}

// passkeyRP returns the relying party from the settings: the configured ID, otherwise the host of
// the public URL; the configured origins plus that of the public URL.
func (a *App) passkeyRP() (webauthn.RP, error) {
	s := a.Settings()
	rp := webauthn.RP{ID: s.PasskeyRPID, Name: s.ServerName, Origins: slices.Clone(s.PasskeyOrigins)}
	if s.PublicURL != "" {
		if u, err := url.Parse(s.PublicURL); err == nil {
			if rp.ID == "" {
				rp.ID = u.Hostname()
			}
			rp.Origins = append(rp.Origins, u.Scheme+"://"+u.Host)
		}
	}
	if rp.ID == "" || len(rp.Origins) == 0 {
		return rp, domain.Precondition("passkey.unavailable")
	}
	return rp, nil
}

// PasskeysAvailable reports whether passkeys can be used (public URL set).
func (a *App) PasskeysAvailable() bool {
	_, err := a.passkeyRP()
	return err == nil
}

// BeginPasskeyRegistration prepares registering a passkey on the caller's account: the request ID
// and the options to pass to the authenticator (JSON).
func (a *App) BeginPasskeyRegistration(ctx context.Context, p domain.Principal) (string, []byte, error) {
	rp, err := a.passkeyRP()
	if err != nil {
		return "", nil, err
	}
	existing, err := a.store.Read().Passkeys(ctx, p.Account.ID)
	if err != nil {
		return "", nil, err
	}
	if len(existing) >= maxPasskeys {
		return "", nil, domain.Precondition("passkey.too_many", "max", maxPasskeys)
	}
	exclude := make([][]byte, len(existing))
	for i, pk := range existing {
		exclude[i] = pk.CredentialID
	}
	account := p.Account.ID
	challenge := webauthn.NewChallenge()
	id, err := a.passkeys.put(passkeyChallenge{challenge: challenge, account: &account, expires: a.now().Add(webauthn.Timeout)}, a.now())
	if err != nil {
		return "", nil, err
	}
	user := webauthn.User{Handle: account[:], Name: p.Account.Username, DisplayName: p.Account.Username}
	return id, webauthn.CreationOptions(rp, user, challenge, exclude), nil
}

// FinishPasskeyRegistration verifies the authenticator's response and registers the passkey under
// the given name ("Passkey" if empty).
func (a *App) FinishPasskeyRegistration(ctx context.Context, p domain.Principal, id string, response []byte, name string) (domain.Passkey, error) {
	name = strings.TrimSpace(name)
	switch {
	case name == "":
		name = "Passkey"
	case utf8.RuneCountInString(name) > maxPasskeyNameLen || !utf8.ValidString(name):
		return domain.Passkey{}, domain.Invalid("passkey.name_too_long", "max", maxPasskeyNameLen)
	}
	ch, ok := a.passkeys.take(id, a.now())
	if !ok || ch.account == nil || *ch.account != p.Account.ID {
		return domain.Passkey{}, domain.Invalid("passkey.challenge_expired")
	}
	rp, err := a.passkeyRP()
	if err != nil {
		return domain.Passkey{}, err
	}
	cred, err := webauthn.VerifyRegistration(rp, ch.challenge, response)
	if err != nil {
		return domain.Passkey{}, domain.Invalid("passkey.rejected", "reason", err)
	}
	pk := domain.Passkey{
		ID: domain.NewID(), AccountID: p.Account.ID, CredentialID: cred.ID, PublicKey: cred.PublicKey,
		SignCount: cred.SignCount, Name: name, CreatedAt: a.now(),
	}
	if err := a.store.Write(ctx, func(q store.Q) error { return q.AddPasskey(ctx, pk) }); err != nil {
		if errors.Is(err, store.ErrDuplicate) {
			return domain.Passkey{}, domain.Conflict("passkey.already_registered")
		}
		return domain.Passkey{}, err
	}
	a.record(ctx, domain.Activity{
		Kind: domain.ActivityAccountUpdated, AccountID: &p.Account.ID,
		Text: domain.T("activity.passkey_added", "name", name, "username", p.Account.Username),
	})
	return pk, nil
}

// Passkeys lists the passkeys of the caller's account.
func (a *App) Passkeys(ctx context.Context, p domain.Principal) ([]domain.Passkey, error) {
	return a.store.Read().Passkeys(ctx, p.Account.ID)
}

// DeletePasskey removes a passkey from the caller's account.
func (a *App) DeletePasskey(ctx context.Context, p domain.Principal, id domain.ID) error {
	err := a.store.Write(ctx, func(q store.Q) error {
		ok, err := q.DeletePasskey(ctx, p.Account.ID, id)
		if err == nil && !ok {
			return domain.NotFound("passkey.not_found")
		}
		return err
	})
	if err == nil {
		a.record(ctx, domain.Activity{
			Kind: domain.ActivityAccountUpdated, AccountID: &p.Account.ID,
			Text: domain.T("activity.passkey_removed", "username", p.Account.Username),
		})
	}
	return err
}

// BeginPasskeyLogin prepares a passkey login without a username: the authenticator offers the
// site's passkeys.
func (a *App) BeginPasskeyLogin() (string, []byte, error) {
	rp, err := a.passkeyRP()
	if err != nil {
		return "", nil, err
	}
	challenge := webauthn.NewChallenge()
	id, err := a.passkeys.put(passkeyChallenge{challenge: challenge, expires: a.now().Add(webauthn.Timeout)}, a.now())
	if err != nil {
		return "", nil, err
	}
	return id, webauthn.RequestOptions(rp, challenge), nil
}

// FinishPasskeyLogin verifies the authenticator's response and opens a session, like a password
// login (a single profile without a PIN is picked automatically).
func (a *App) FinishPasskeyLogin(ctx context.Context, id string, response []byte, dev domain.Device, ip string) (Login, error) {
	keys := []string{"ip:" + ip}
	if wait, ok := a.limiter.Allow(keys...); !ok {
		return Login{}, domain.TooManyAttempts("auth.too_many_attempts", "retry_after_seconds", roundUp(wait))
	}
	fail := func(accountID *domain.ID, why string) (Login, error) {
		a.limiter.Fail(keys...)
		a.recordLoginFailure(ctx, accountID, domain.T("activity.passkey_login_failed", "reason", why, "device", cleanDevice(dev).Name, "ip", ipOrUnknown(ip)))
		// The refusal does not say why (unknown passkey, bad signature...).
		return Login{}, domain.Unauthenticated("passkey.login_failed")
	}
	ch, ok := a.passkeys.take(id, a.now())
	if !ok || ch.account != nil {
		return Login{}, domain.Invalid("passkey.challenge_expired")
	}
	rp, err := a.passkeyRP()
	if err != nil {
		return Login{}, err
	}
	credID, handle, err := webauthn.CredentialID(response)
	if err != nil {
		return fail(nil, "unreadable response")
	}
	read := a.store.Read()
	pk, err := read.PasskeyByCredential(ctx, credID)
	if store.IsNotFound(err) {
		return fail(nil, "unknown passkey")
	}
	if err != nil {
		return Login{}, err
	}
	if handle != nil && !bytes.Equal(handle, pk.AccountID[:]) {
		return fail(&pk.AccountID, "passkey of another account")
	}
	count, err := webauthn.VerifyAssertion(rp, ch.challenge, webauthn.Credential{ID: pk.CredentialID, PublicKey: pk.PublicKey, SignCount: pk.SignCount}, response)
	if err != nil {
		return fail(&pk.AccountID, err.Error())
	}
	a.limiter.Succeed(keys...)
	account, err := read.Account(ctx, pk.AccountID)
	if err != nil {
		return Login{}, err
	}
	if account.Disabled {
		a.recordLoginFailure(ctx, &account.ID, domain.T("activity.login_failed_disabled", "username", account.Username, "device", cleanDevice(dev).Name, "ip", ipOrUnknown(ip)))
		return Login{}, domain.Forbidden("auth.account_disabled")
	}
	var login Login
	err = a.store.Write(ctx, func(q store.Q) error {
		if err := q.TouchPasskey(ctx, pk.ID, count, a.now()); err != nil {
			return err
		}
		login, err = a.openSession(ctx, q, account, dev, ip)
		return err
	})
	if err == nil {
		d := login.Session.Session.Device
		a.record(ctx, domain.Activity{
			Kind: domain.ActivityLogin, AccountID: &account.ID,
			Text: domain.T("activity.login_by_passkey", "username", account.Username, "passkey", pk.Name, "device", d.Name, "client", d.Client, "ip", ipOrUnknown(ip)),
		})
	}
	return login, err
}
