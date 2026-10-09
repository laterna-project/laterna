package app

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/laterna-project/laterna/internal/auth"
	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/store"
)

const (
	// sessionLifetime: a session left unused for this long expires. Each use extends it.
	sessionLifetime = 90 * 24 * time.Hour
	// touchInterval: the last use is only written once per interval, to avoid a database write on
	// every request.
	touchInterval  = 10 * time.Minute
	minPasswordLen = 8
	maxPasswordLen = 1024
	maxUsernameLen = 64
	maxDeviceField = 128
)

// Login is the result of signing in: the session created and its token, to hand to the client only
// once.
type Login struct {
	Session SessionDetails
	Token   string
}

// SessionDetails describes a session with its account and picked profile.
type SessionDetails struct {
	Session domain.Session
	Account domain.Account
	Profile *domain.Profile
}

// SetupRequired reports that no account exists yet.
func (a *App) SetupRequired(ctx context.Context) (bool, error) {
	n, err := a.store.Read().CountAccounts(ctx)
	return n == 0, err
}

// Setup creates the first account, an administrator, with a profile of the same name, and signs it
// in.
func (a *App) Setup(ctx context.Context, username, password string, dev domain.Device, ip string) (Login, error) {
	username = strings.TrimSpace(username)
	if err := validateUsername(username); err != nil {
		return Login{}, err
	}
	if err := validatePassword(password); err != nil {
		return Login{}, err
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return Login{}, err
	}
	now := a.now()
	account := domain.Account{
		ID: domain.NewID(), Username: username, IsAdmin: true, Libraries: domain.AllLibraries(), RequestQuota: domain.DefaultRequestQuota,
		CreatedAt: now, UpdatedAt: now,
	}
	profile := domain.Profile{ID: domain.NewID(), AccountID: account.ID, Name: username, CreatedAt: now, UpdatedAt: now}
	var login Login
	err = a.store.Write(ctx, func(q store.Q) error {
		// Inside the write transaction: two setups at the same time cannot both go through.
		n, err := q.CountAccounts(ctx)
		if err != nil {
			return err
		}
		if n > 0 {
			return domain.Precondition("auth.already_set_up")
		}
		if err := q.CreateAccount(ctx, account, hash); err != nil {
			return err
		}
		if err := q.CreateProfile(ctx, profile, ""); err != nil {
			return err
		}
		login, err = a.openSession(ctx, q, account, dev, ip)
		return err
	})
	if err != nil {
		return Login{}, err
	}
	a.log.InfoContext(ctx, "setup complete: administrator account created", "username", username)
	a.record(ctx, domain.Activity{
		Kind: domain.ActivityAccountCreated, AccountID: &account.ID,
		Text: domain.T("activity.setup", "username", username),
	})
	return login, nil
}

// Login checks the credentials and opens a session for the device.
func (a *App) Login(ctx context.Context, username, password string, dev domain.Device, ip string) (Login, error) {
	keys := []string{"account:" + store.NameKey(username), "ip:" + ip}
	if wait, ok := a.limiter.Allow(keys...); !ok {
		return Login{}, domain.TooManyAttempts("auth.too_many_attempts", "retry_after_seconds", roundUp(wait))
	}
	account, hash, err := a.store.Read().AccountByUsername(ctx, username)
	if store.IsNotFound(err) {
		// Same computation time as for an existing account: the response time gives nothing away.
		auth.BurnTime(password)
		a.limiter.Fail(keys...)
		a.recordLoginFailure(ctx, nil, domain.T("activity.login_failed", "username", strings.TrimSpace(username), "device", cleanDevice(dev).Name, "ip", ipOrUnknown(ip)))
		return Login{}, domain.Unauthenticated("auth.invalid_credentials")
	}
	if err != nil {
		return Login{}, err
	}
	ok, rehash, err := auth.VerifyPassword(hash, password)
	if err != nil {
		return Login{}, fmt.Errorf("account %s: %w", account.ID, err)
	}
	if !ok {
		a.limiter.Fail(keys...)
		a.recordLoginFailure(ctx, &account.ID, domain.T("activity.login_failed", "username", strings.TrimSpace(username), "device", cleanDevice(dev).Name, "ip", ipOrUnknown(ip)))
		return Login{}, domain.Unauthenticated("auth.invalid_credentials")
	}
	a.limiter.Succeed(keys...)
	if account.Disabled {
		a.recordLoginFailure(ctx, &account.ID, domain.T("activity.login_failed_disabled", "username", account.Username, "device", cleanDevice(dev).Name, "ip", ipOrUnknown(ip)))
		return Login{}, domain.Forbidden("auth.account_disabled")
	}

	var login Login
	err = a.store.Write(ctx, func(q store.Q) error {
		if rehash {
			// Hashing parameters got stronger since: make use of having the password in clear.
			if newHash, err := auth.HashPassword(password); err == nil {
				if err := q.SetPassword(ctx, account.ID, newHash, a.now()); err != nil {
					return err
				}
			}
		}
		login, err = a.openSession(ctx, q, account, dev, ip)
		return err
	})
	if err == nil {
		d := login.Session.Session.Device
		a.record(ctx, domain.Activity{
			Kind: domain.ActivityLogin, AccountID: &account.ID,
			Text: domain.T("activity.login", "username", account.Username, "device", d.Name, "client", d.Client, "ip", ipOrUnknown(ip)),
		})
	}
	return login, err
}

// recordLoginFailure records a refused login (accountID is nil for an unknown account). what tells
// the refusal.
func (a *App) recordLoginFailure(ctx context.Context, accountID *domain.ID, what domain.Text) {
	a.record(ctx, domain.Activity{Kind: domain.ActivityLoginFailed, Warning: true, AccountID: accountID, Text: what})
}

// ipOrUnknown writes an address into a text: "?" if it is unknown.
func ipOrUnknown(ip string) string {
	if ip == "" {
		return "?"
	}
	return ip
}

// openSession creates the session and picks the profile automatically if there is only one and it
// has no PIN.
func (a *App) openSession(ctx context.Context, q store.Q, account domain.Account, dev domain.Device, ip string) (Login, error) {
	token, tokenHash, err := auth.NewToken()
	if err != nil {
		return Login{}, err
	}
	now := a.now()
	s := domain.Session{
		ID: domain.NewID(), AccountID: account.ID, Device: cleanDevice(dev),
		CreatedAt: now, LastUsedAt: now, ExpiresAt: now.Add(sessionLifetime), LastIP: ip,
	}
	if err := q.CreateSession(ctx, s, tokenHash); err != nil {
		return Login{}, err
	}
	details := SessionDetails{Session: s, Account: account}
	profiles, err := q.Profiles(ctx, account.ID)
	if err != nil {
		return Login{}, err
	}
	if len(profiles) == 1 && !profiles[0].HasPIN {
		if err := q.SetSessionProfile(ctx, s.ID, profiles[0].ID); err != nil {
			return Login{}, err
		}
		details.Session.ProfileID = &profiles[0].ID
		details.Profile = &profiles[0]
	}
	return Login{Session: details, Token: token}, nil
}

// Authenticate finds the caller from their token and extends their session.
func (a *App) Authenticate(ctx context.Context, token, ip string) (domain.Principal, error) {
	if token == "" {
		return domain.Principal{}, domain.Unauthenticated("auth.required")
	}
	read := a.store.Read()
	s, err := read.SessionByTokenHash(ctx, auth.HashToken(token))
	now := a.now()
	if store.IsNotFound(err) || (err == nil && !now.Before(s.ExpiresAt)) {
		return domain.Principal{}, domain.Unauthenticated("auth.session_expired")
	}
	if err != nil {
		return domain.Principal{}, err
	}
	account, err := read.Account(ctx, s.AccountID)
	if err != nil {
		return domain.Principal{}, err
	}
	if account.Disabled {
		return domain.Principal{}, domain.Unauthenticated("auth.account_disabled")
	}
	p := domain.Principal{SessionID: s.ID, Account: account}
	if s.ProfileID != nil {
		profile, _, err := read.Profile(ctx, *s.ProfileID)
		if err != nil {
			return domain.Principal{}, err
		}
		p.Profile = &profile
	}
	if now.Sub(s.LastUsedAt) >= touchInterval || ip != s.LastIP {
		if err := a.store.Write(ctx, func(q store.Q) error {
			return q.TouchSession(ctx, s.ID, now, now.Add(sessionLifetime), ip)
		}); err != nil {
			// A valid session stays valid even if the timestamp could not be written.
			a.log.WarnContext(ctx, "cannot update session", "session", s.ID, "err", err)
		}
	}
	return p, nil
}

// Logout closes the caller's session.
func (a *App) Logout(ctx context.Context, p domain.Principal) error {
	return a.store.Write(ctx, func(q store.Q) error { return q.DeleteSession(ctx, p.SessionID) })
}

// Session describes the caller's session.
func (a *App) Session(ctx context.Context, p domain.Principal) (SessionDetails, error) {
	s, err := a.store.Read().Session(ctx, p.SessionID)
	if err != nil {
		return SessionDetails{}, err
	}
	return SessionDetails{Session: s, Account: p.Account, Profile: p.Profile}, nil
}

// Sessions lists the devices signed in to the caller's account.
func (a *App) Sessions(ctx context.Context, p domain.Principal) ([]SessionDetails, error) {
	read := a.store.Read()
	sessions, err := read.Sessions(ctx, p.Account.ID, a.now())
	if err != nil {
		return nil, err
	}
	profiles, err := read.Profiles(ctx, p.Account.ID)
	if err != nil {
		return nil, err
	}
	byID := make(map[domain.ID]*domain.Profile, len(profiles))
	for i := range profiles {
		byID[profiles[i].ID] = &profiles[i]
	}
	out := make([]SessionDetails, len(sessions))
	for i, s := range sessions {
		out[i] = SessionDetails{Session: s, Account: p.Account}
		if s.ProfileID != nil {
			out[i].Profile = byID[*s.ProfileID]
		}
	}
	return out, nil
}

// RevokeSession signs a device out of the caller's account.
func (a *App) RevokeSession(ctx context.Context, p domain.Principal, sessionID domain.ID) error {
	if p.Restricted() {
		return domain.Forbidden("auth.restricted_profile")
	}
	return a.store.Write(ctx, func(q store.Q) error {
		s, err := q.Session(ctx, sessionID)
		if store.IsNotFound(err) || (err == nil && s.AccountID != p.Account.ID) {
			return domain.NotFound("auth.session_not_found")
		}
		if err != nil {
			return err
		}
		return q.DeleteSession(ctx, sessionID)
	})
}

// ChangePassword changes the caller's password and signs out their other devices.
func (a *App) ChangePassword(ctx context.Context, p domain.Principal, current, next string) error {
	if p.Restricted() {
		return domain.Forbidden("auth.restricted_profile")
	}
	key := "account:" + store.NameKey(p.Account.Username)
	if wait, ok := a.limiter.Allow(key); !ok {
		return domain.TooManyAttempts("auth.too_many_attempts", "retry_after_seconds", roundUp(wait))
	}
	hash, err := a.store.Read().PasswordHash(ctx, p.Account.ID)
	if err != nil {
		return err
	}
	ok, _, err := auth.VerifyPassword(hash, current)
	if err != nil {
		return err
	}
	if !ok {
		a.limiter.Fail(key)
		return domain.Unauthenticated("auth.wrong_password")
	}
	a.limiter.Succeed(key)
	if err := validatePassword(next); err != nil {
		return err
	}
	newHash, err := auth.HashPassword(next)
	if err != nil {
		return err
	}
	return a.store.Write(ctx, func(q store.Q) error {
		if err := q.SetPassword(ctx, p.Account.ID, newHash, a.now()); err != nil {
			return err
		}
		return q.DeleteOtherSessions(ctx, p.Account.ID, p.SessionID)
	})
}

// PurgeExpiredSessions deletes expired sessions.
func (a *App) PurgeExpiredSessions(ctx context.Context) (int64, error) {
	var n int64
	err := a.store.Write(ctx, func(q store.Q) error {
		var err error
		n, err = q.DeleteExpiredSessions(ctx, a.now())
		return err
	})
	return n, err
}

func validateUsername(name string) error {
	if name == "" {
		return domain.Invalid("auth.username_required")
	}
	if utf8.RuneCountInString(name) > maxUsernameLen {
		return domain.Invalid("auth.username_too_long", "max", maxUsernameLen)
	}
	if strings.ContainsFunc(name, unicode.IsControl) {
		return domain.Invalid("auth.username_invalid")
	}
	return nil
}

func validatePassword(pw string) error {
	if utf8.RuneCountInString(pw) < minPasswordLen {
		return domain.Invalid("auth.password_too_short", "min", minPasswordLen)
	}
	if len(pw) > maxPasswordLen {
		return domain.Invalid("auth.password_too_long")
	}
	return nil
}

// cleanDevice bounds and cleans up the fields supplied by the client.
func cleanDevice(d domain.Device) domain.Device {
	clean := func(s, def string) string {
		s = strings.TrimSpace(strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return -1
			}
			return r
		}, s))
		if s == "" {
			return def
		}
		if len(s) > maxDeviceField {
			s = strings.ToValidUTF8(s[:maxDeviceField], "")
		}
		return s
	}
	return domain.Device{
		Name:          clean(d.Name, "Unknown device"),
		Client:        clean(d.Client, "Unknown"),
		ClientVersion: clean(d.ClientVersion, ""),
		Platform:      clean(d.Platform, ""),
	}
}

// roundUp rounds a wait up to the minute for display.
func roundUp(d time.Duration) time.Duration {
	return max(time.Minute, d.Round(time.Minute))
}

// Rate of the public endpoints per address: 10 at once, then one every 6 s (10 a minute). A
// household signing in never hits the limit; a bot does.
const (
	publicBurst = 10
	publicEvery = 6 * time.Second
)

// ThrottlePublic slows down the public endpoints that are expensive (login, starting a device,
// passkey or provider login): past the rate allowed for this address it returns RESOURCE_EXHAUSTED
// with the time to wait.
func (a *App) ThrottlePublic(ip string) error {
	if wait, ok := a.publicRate.Allow("ip:" + ip); !ok {
		return domain.TooManyAttempts("request.rate_limited", "retry_after_seconds", wait)
	}
	return nil
}
