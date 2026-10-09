package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/laterna-project/laterna/internal/auth"
	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/oidc"
	"github.com/laterna-project/laterna/internal/store"
)

// OpenID Connect login. The client asks for an authorization URL (StartOIDCLogin) and opens it in a
// browser. The user signs in at the provider, which sends them back to the server's callback
// (/auth/oidc/callback). The server exchanges the code, verifies the ID token and finds the account
// (CompleteOIDCLogin), then asks in the browser to confirm the device (ConfirmOIDCLogin) before
// opening the session. The client gets it by polling PollOIDCLogin. The same flow serves web,
// mobile and TV.

const (
	keyOIDC = "auth.oidc"
	// OIDCCallbackPath is the callback path, under the server's public URL.
	OIDCCallbackPath = "/auth/oidc/callback"
	oidcLoginFor     = 10 * time.Minute
	// OIDCPollInterval is the suggested time between two polls.
	OIDCPollInterval  = 2 * time.Second
	maxPendingOIDC    = 10000
	oidcDefaultButton = "OpenID Connect"
)

// OIDCLoginState is the state of an OIDC login.
type OIDCLoginState string

// States of an OIDC login.
const (
	OIDCPending  OIDCLoginState = "pending"
	OIDCApproved OIDCLoginState = "approved"
	OIDCDenied   OIDCLoginState = "denied"
	OIDCExpired  OIDCLoginState = "expired"
)

// oidcState holds the provider setting, the discovered provider and the logins in progress.
type oidcState struct {
	mu        sync.Mutex
	cfg       domain.OIDCProvider
	provider  *oidc.Provider
	byID      map[string]*oidcLogin // by hash of the secret login ID
	byState   map[string]*oidcLogin
	byConfirm map[string]*oidcLogin // by hash of the confirmation token
}

type oidcLogin struct {
	idHash, state, nonce, verifier, redirect string
	device                                   domain.Device
	ip                                       string
	expires                                  time.Time
	// Back from the provider: identity verified, waiting for confirmation.
	confirmHash string
	claims      *oidc.Claims
	login       *Login
	denied      domain.Text
}

// forget drops a login. Call it with o.mu held.
func (o *oidcState) forget(l *oidcLogin) {
	delete(o.byID, l.idHash)
	delete(o.byState, l.state)
	if l.confirmHash != "" {
		delete(o.byConfirm, l.confirmHash)
	}
}

func oidcHash(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:])
}

// loadOIDC reads the provider setting at startup.
func (a *App) loadOIDC(ctx context.Context) error {
	raw, ok, err := a.store.Read().Setting(ctx, keyOIDC)
	if err != nil || !ok {
		return err
	}
	var cfg domain.OIDCProvider
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return fmt.Errorf("unreadable setting %s: %w", keyOIDC, err)
	}
	a.oidc.mu.Lock()
	a.oidc.cfg = cfg
	a.oidc.mu.Unlock()
	return nil
}

// OIDCProvider returns the provider setting (client secret included: the API must never return it).
func (a *App) OIDCProvider() domain.OIDCProvider {
	a.oidc.mu.Lock()
	defer a.oidc.mu.Unlock()
	return a.oidc.cfg
}

// OIDCButton returns the label of the sign-in button if OIDC login is possible (provider and public
// URL both set), "" otherwise.
func (a *App) OIDCButton() string {
	cfg := a.OIDCProvider()
	if cfg.Issuer == "" || a.Settings().PublicURL == "" {
		return ""
	}
	if cfg.Name == "" {
		return oidcDefaultButton
	}
	return cfg.Name
}

// SetOIDCProvider sets the provider (an empty issuer turns OIDC login off). It is discovered before
// being stored. An empty secret keeps the one of the same client.
func (a *App) SetOIDCProvider(ctx context.Context, p domain.Principal, cfg domain.OIDCProvider) (domain.OIDCProvider, error) {
	cfg.Issuer = strings.TrimRight(strings.TrimSpace(cfg.Issuer), "/")
	cfg.ClientID, cfg.Name = strings.TrimSpace(cfg.ClientID), strings.TrimSpace(cfg.Name)
	var provider *oidc.Provider
	if cfg.Issuer != "" {
		if cfg.ClientID == "" {
			return domain.OIDCProvider{}, domain.Invalid("oidc.client_id_required")
		}
		if old := a.OIDCProvider(); cfg.ClientSecret == "" && old.Issuer == cfg.Issuer && old.ClientID == cfg.ClientID {
			cfg.ClientSecret = old.ClientSecret
		}
		if cfg.ClientSecret == "" {
			return domain.OIDCProvider{}, domain.Invalid("oidc.client_secret_required")
		}
		var err error
		if provider, err = oidc.Discover(ctx, a.http, cfg.Issuer); err != nil {
			return domain.OIDCProvider{}, domain.Invalid("oidc.provider_unreachable", "reason", err)
		}
	} else {
		cfg = domain.OIDCProvider{}
	}
	// The client secret is stored as is, since it has to be sent with every code exchange.
	raw, err := json.Marshal(cfg) //nolint:gosec // G117: see above
	if err != nil {
		return domain.OIDCProvider{}, err
	}
	if err := a.store.Write(ctx, func(q store.Q) error { return q.SetSetting(ctx, keyOIDC, string(raw)) }); err != nil {
		return domain.OIDCProvider{}, err
	}
	a.oidc.mu.Lock()
	a.oidc.cfg, a.oidc.provider = cfg, provider
	a.oidc.mu.Unlock()
	summary := domain.T("activity.oidc_disabled")
	if cfg.Issuer != "" {
		summary = domain.T("activity.oidc_configured", "issuer", cfg.Issuer, "client_id", cfg.ClientID)
	}
	a.record(ctx, domain.Activity{Kind: domain.ActivitySettingsUpdated, AccountID: &p.Account.ID, Text: summary})
	return cfg, nil
}

// oidcClient returns the setting, the provider (discovered if needed) and the callback URL.
func (a *App) oidcClient(ctx context.Context) (domain.OIDCProvider, *oidc.Provider, string, error) {
	public := a.Settings().PublicURL
	a.oidc.mu.Lock()
	cfg, provider := a.oidc.cfg, a.oidc.provider
	a.oidc.mu.Unlock()
	if cfg.Issuer == "" || public == "" {
		return cfg, nil, "", domain.Precondition("oidc.unavailable")
	}
	if provider == nil {
		var err error
		if provider, err = oidc.Discover(ctx, a.http, cfg.Issuer); err != nil {
			return cfg, nil, "", domain.Precondition("oidc.provider_unreachable", "reason", err)
		}
		a.oidc.mu.Lock()
		if a.oidc.cfg.Issuer == cfg.Issuer {
			a.oidc.provider = provider
		}
		a.oidc.mu.Unlock()
	}
	return cfg, provider, public + OIDCCallbackPath, nil
}

// OIDCLoginStart is an OIDC login that was opened: the secret ID to give PollOIDCLogin and the URL
// to open in a browser.
type OIDCLoginStart struct {
	LoginID, AuthURL    string
	Interval, ExpiresIn time.Duration
}

// StartOIDCLogin opens an OIDC login for a device.
func (a *App) StartOIDCLogin(ctx context.Context, dev domain.Device, ip string) (OIDCLoginStart, error) {
	dev = cleanDevice(dev)
	if dev.Name == "" {
		return OIDCLoginStart{}, domain.Invalid("auth.device_name_required")
	}
	cfg, provider, redirect, err := a.oidcClient(ctx)
	if err != nil {
		return OIDCLoginStart{}, err
	}
	now := a.now()
	o := &a.oidc
	o.mu.Lock()
	defer o.mu.Unlock()
	a.purgeOIDCLogins(ctx, now)
	if len(o.byID) >= maxPendingOIDC {
		return OIDCLoginStart{}, domain.TooManyAttempts("oidc.too_many_pending")
	}
	id := oidc.Secret()
	l := &oidcLogin{
		idHash: oidcHash(id), state: oidc.Secret(), nonce: oidc.Secret(), verifier: oidc.Secret(), redirect: redirect,
		device: dev, ip: ip, expires: now.Add(oidcLoginFor),
	}
	if o.byID == nil {
		o.byID, o.byState, o.byConfirm = map[string]*oidcLogin{}, map[string]*oidcLogin{}, map[string]*oidcLogin{}
	}
	o.byID[l.idHash], o.byState[l.state] = l, l
	return OIDCLoginStart{
		LoginID: id, AuthURL: provider.AuthCodeURL(cfg.ClientID, redirect, l.state, l.nonce, l.verifier),
		Interval: OIDCPollInterval, ExpiresIn: oidcLoginFor,
	}, nil
}

// purgeOIDCLogins forgets expired logins. A session that was opened but never handed over is
// closed. Call it with a.oidc.mu held.
func (a *App) purgeOIDCLogins(ctx context.Context, now time.Time) {
	for _, l := range a.oidc.byID {
		if now.Before(l.expires) {
			continue
		}
		a.oidc.forget(l)
		if l.login != nil {
			id := l.login.Session.Session.ID
			if err := a.store.Write(ctx, func(q store.Q) error { return q.DeleteSession(ctx, id) }); err != nil {
				a.log.WarnContext(ctx, "session of an OIDC sign-in never delivered: cannot close it", "session", id, "err", err)
			}
		}
	}
}

// OIDCCallback is what the callback page shows: a refusal (Message), or a request to confirm the
// device that will get the session (Confirm, a token to send back to ConfirmOIDCLogin).
type OIDCCallback struct {
	Message domain.Text
	Confirm string
	Account string // username of the account (to be created if it does not exist yet)
	Device  domain.Device
	IP      string
}

// oidcUnknown means the login does not exist, or not anymore.
var oidcUnknown = domain.T("oidc.unknown_request")

// CompleteOIDCLogin handles the provider's callback: code exchange, token verification, account
// lookup. The session is only opened after confirmation in the browser. Without that step, an
// authorization link sent by someone else would be enough to sign their device in to the account of
// whoever clicks, since the provider redirects a user who is already signed in there without asking
// anything.
func (a *App) CompleteOIDCLogin(ctx context.Context, state, code, providerError string) OIDCCallback {
	now := a.now()
	a.oidc.mu.Lock()
	l, ok := a.oidc.byState[state]
	if ok {
		delete(a.oidc.byState, state) // only once
	}
	a.oidc.mu.Unlock()
	if !ok || now.After(l.expires) {
		return OIDCCallback{Message: oidcUnknown}
	}
	deny := func(msg domain.Text) OIDCCallback {
		a.denyOIDCLogin(ctx, l, msg)
		return OIDCCallback{Message: msg}
	}
	if providerError != "" {
		return deny(domain.T("oidc.provider_refused", "reason", providerError))
	}
	cfg, provider, _, err := a.oidcClient(ctx)
	if err != nil {
		return deny(a.oidcErrorText(ctx, err))
	}
	token, err := provider.Exchange(ctx, cfg.ClientID, cfg.ClientSecret, code, l.verifier, l.redirect)
	if err != nil {
		a.log.WarnContext(ctx, "OIDC: code exchange refused", "err", err)
		return deny(domain.T("oidc.exchange_refused"))
	}
	claims, err := provider.Verify(ctx, token, cfg.ClientID, l.nonce, now)
	if err != nil {
		a.log.WarnContext(ctx, "OIDC: ID token rejected", "err", err)
		return deny(domain.T("oidc.token_refused", "reason", err))
	}
	account, err := a.oidcAccount(ctx, cfg, claims, false)
	if err != nil {
		return deny(a.oidcErrorText(ctx, err))
	}
	confirm := oidc.Secret()
	a.oidc.mu.Lock()
	defer a.oidc.mu.Unlock()
	if a.oidc.byID[l.idHash] != l { // forgotten in the meantime
		return OIDCCallback{Message: oidcUnknown}
	}
	l.claims, l.confirmHash = &claims, oidcHash(confirm)
	a.oidc.byConfirm[l.confirmHash] = l
	return OIDCCallback{Confirm: confirm, Account: account.Username, Device: l.device, IP: ipOrUnknown(l.ip)}
}

// ConfirmOIDCLogin applies the answer given in the browser: refusal, or account linked (or created)
// and session opened for the device. It returns the message to show and whether the login
// succeeded.
func (a *App) ConfirmOIDCLogin(ctx context.Context, confirm string, approve bool) (domain.Text, bool) {
	now := a.now()
	a.oidc.mu.Lock()
	l, ok := a.oidc.byConfirm[oidcHash(confirm)]
	if ok {
		delete(a.oidc.byConfirm, l.confirmHash) // only once
		l.confirmHash = ""
	}
	a.oidc.mu.Unlock()
	if !ok || now.After(l.expires) || l.claims == nil {
		return oidcUnknown, false
	}
	deny := func(msg domain.Text) (domain.Text, bool) {
		a.denyOIDCLogin(ctx, l, msg)
		return msg, false
	}
	if !approve {
		return deny(domain.T("oidc.denied_in_browser"))
	}
	account, err := a.oidcAccount(ctx, a.OIDCProvider(), *l.claims, true)
	if err != nil {
		return deny(a.oidcErrorText(ctx, err))
	}
	var login Login
	if err := a.store.Write(ctx, func(q store.Q) error {
		var err error
		login, err = a.openSession(ctx, q, account, l.device, l.ip)
		return err
	}); err != nil {
		a.log.ErrorContext(ctx, "OIDC: cannot open session", "err", err)
		return deny(domain.T("oidc.server_error"))
	}
	a.oidc.mu.Lock()
	l.login = &login
	a.oidc.mu.Unlock()
	a.record(ctx, domain.Activity{
		Kind: domain.ActivityLogin, AccountID: &account.ID,
		Text: domain.T("activity.login_by_oidc", "username", account.Username, "provider", a.OIDCButton(), "device", l.device.Name, "client", l.device.Client, "ip", ipOrUnknown(l.ip)),
	})
	return domain.T("oidc.success"), true
}

// denyOIDCLogin records that a login was refused, which the device will learn when it polls.
func (a *App) denyOIDCLogin(ctx context.Context, l *oidcLogin, msg domain.Text) {
	a.oidc.mu.Lock()
	l.denied = msg
	a.oidc.mu.Unlock()
	a.recordLoginFailure(ctx, nil, domain.T("activity.oidc_login_failed", "device", cleanDevice(l.device).Name, "ip", ipOrUnknown(l.ip), []domain.Text{msg}))
}

// oidcErrorText returns the text of an expected error (account not found or refused, provider
// unreachable). Any other error is logged and not shown.
func (a *App) oidcErrorText(ctx context.Context, err error) domain.Text {
	var de *domain.Error
	if errors.As(err, &de) {
		return de.Text()
	}
	a.log.ErrorContext(ctx, "OIDC: account not found", "err", err)
	return domain.T("oidc.server_error")
}

// oidcAccount finds the account of a provider identity: the one linked to it, otherwise the one
// with the same username (which gets linked), otherwise a new account if the setting allows it.
// Without commit nothing is written: the account to create only has a name.
func (a *App) oidcAccount(ctx context.Context, cfg domain.OIDCProvider, c oidc.Claims, commit bool) (domain.Account, error) {
	read := a.store.Read()
	id, err := read.OIDCAccount(ctx, c.Issuer, c.Subject)
	if err == nil {
		return a.usableAccount(ctx, id)
	}
	if !store.IsNotFound(err) {
		return domain.Account{}, err
	}
	username := strings.TrimSpace(c.Username)
	if username == "" {
		username = strings.TrimSpace(c.Email)
	}
	if err := validateUsername(username); err != nil {
		return domain.Account{}, domain.Invalid("oidc.no_username")
	}
	now := a.now()
	account, _, err := read.AccountByUsername(ctx, username)
	switch {
	case err == nil:
		if account.Disabled {
			return domain.Account{}, domain.Forbidden("auth.account_disabled")
		}
		if !commit {
			return account, nil
		}
		err = a.store.Write(ctx, func(q store.Q) error { return q.LinkOIDC(ctx, c.Issuer, c.Subject, account.ID, now) })
		return account, linkError(err)
	case !store.IsNotFound(err):
		return domain.Account{}, err
	case !cfg.AutoCreate:
		return domain.Account{}, domain.NotFound("oidc.no_account", "username", username)
	case !commit:
		return domain.Account{Username: username}, nil
	}
	// Created account: no usable password (a random secret that is never shown).
	hash, err := auth.HashPassword(randomPassword())
	if err != nil {
		return domain.Account{}, err
	}
	account = domain.Account{
		ID: domain.NewID(), Username: username, Libraries: domain.AllLibraries(), RequestQuota: domain.DefaultRequestQuota,
		CreatedAt: now, UpdatedAt: now,
	}
	profile := domain.Profile{ID: domain.NewID(), AccountID: account.ID, Name: username, CreatedAt: now, UpdatedAt: now}
	err = a.store.Write(ctx, func(q store.Q) error {
		if err := accountWriteError(q.CreateAccount(ctx, account, hash), username); err != nil {
			return err
		}
		if err := q.CreateProfile(ctx, profile, ""); err != nil {
			return err
		}
		return q.LinkOIDC(ctx, c.Issuer, c.Subject, account.ID, now)
	})
	if err != nil {
		return domain.Account{}, linkError(err)
	}
	a.record(ctx, domain.Activity{
		Kind: domain.ActivityAccountCreated, AccountID: &account.ID,
		Text: domain.T("activity.account_created_by_oidc", "username", username, "provider", a.OIDCButton()),
	})
	return account, nil
}

func (a *App) usableAccount(ctx context.Context, id domain.ID) (domain.Account, error) {
	account, err := a.store.Read().Account(ctx, id)
	if err != nil {
		return domain.Account{}, err
	}
	if account.Disabled {
		return domain.Account{}, domain.Forbidden("auth.account_disabled")
	}
	return account, nil
}

// linkError: an identity linked in the meantime by another login (two confirmations at once) is a
// conflict, not a server error.
func linkError(err error) error {
	if errors.Is(err, store.ErrDuplicate) {
		return domain.Conflict("oidc.identity_just_linked")
	}
	return err
}

// randomPassword makes up the password of an account that has no usable one (created through OpenID
// Connect, or imported from Jellyfin without a password). It is never shown and only fills in the
// hash.
func randomPassword() string { return oidc.Secret() + oidc.Secret() }

// PollOIDCLogin returns the state of an OIDC login and, once approved, the session (handed over
// only once), or the message of a refusal.
func (a *App) PollOIDCLogin(ctx context.Context, loginID string) (OIDCLoginState, *Login, domain.Text) {
	now := a.now()
	o := &a.oidc
	o.mu.Lock()
	defer o.mu.Unlock()
	a.purgeOIDCLogins(ctx, now)
	l, ok := o.byID[oidcHash(loginID)]
	switch {
	case !ok:
		return OIDCExpired, nil, domain.Text{}
	case l.login != nil:
		o.forget(l)
		return OIDCApproved, l.login, domain.Text{}
	case !l.denied.IsZero():
		o.forget(l)
		return OIDCDenied, nil, l.denied
	}
	return OIDCPending, nil, domain.Text{}
}
