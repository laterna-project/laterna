package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/store"
)

// Device login (modeled on RFC 8628): a device without a convenient keyboard (TV, console) asks for
// a code and shows it. The user, signed in on their phone, types it and approves. The device, which
// polls the server at regular intervals, then gets its session. Requests live in memory for ten
// minutes, and a restart wipes them.

const (
	// deviceLoginLifetime is how long a code is valid.
	deviceLoginLifetime = 10 * time.Minute
	// deviceLoginInterval is the minimum time between two polls. It grows by 5 s for each poll that
	// comes too soon (RFC 8628, "slow_down").
	deviceLoginInterval = 5 * time.Second
	// userCodeAlphabet has consonants only (RFC 8628, section 6.1): no words, no 0/O or 1/I
	// mix-ups.
	userCodeAlphabet = "BCDFGHJKLMNPQRSTVWXZ"
	userCodeLen      = 8
	// maxDeviceLogins caps the requests in progress, maxDeviceLoginsPerIP those of one address.
	maxDeviceLogins      = 1000
	maxDeviceLoginsPerIP = 10
)

// DeviceLoginState is the state of a device login request.
type DeviceLoginState string

// States of a request.
const (
	DeviceLoginPending  DeviceLoginState = "pending"
	DeviceLoginApproved DeviceLoginState = "approved"
	DeviceLoginDenied   DeviceLoginState = "denied"
	DeviceLoginExpired  DeviceLoginState = "expired"
)

// DeviceLoginStart is what the device gets when it asks for a code.
type DeviceLoginStart struct {
	// DeviceCode is the device's secret, used to poll. UserCode is the one to show ("BDWP-HQPK").
	DeviceCode string
	UserCode   string
	ExpiresAt  time.Time
	Interval   time.Duration
	// VerificationURL is the page of the web client where the code is approved.
	// VerificationURLComplete is the same with the code included (for a QR code). Both are empty if
	// the web client's address is not known.
	VerificationURL         string
	VerificationURLComplete string
}

// DeviceLoginPath is the path, in the web client, of the page where a device login code is
// approved; the code comes in the "code" query parameter. The server builds the URL so that a TV
// does not need to know the web client's routes.
const DeviceLoginPath = "/device"

// verificationURLs builds the URLs where a code is approved: under the web client's address (the
// WebURL setting), otherwise under the server's (PublicURL, for a web client served from the same
// place). They are empty if neither is set, in which case the device shows the code alone.
func (a *App) verificationURLs(userCode string) (page, complete string) {
	s := a.Settings()
	base := s.WebURL
	if base == "" {
		base = s.PublicURL
	}
	if base == "" {
		return "", ""
	}
	page = base + DeviceLoginPath
	return page, page + "?code=" + url.QueryEscape(userCode)
}

// DeviceLoginPoll is the answer to a poll: the state and, once approved, the session (handed over
// only once).
type DeviceLoginPoll struct {
	State    DeviceLoginState
	Login    *Login
	Interval time.Duration
}

// DeviceLoginRequest is what the approving user sees: the device that is asking.
type DeviceLoginRequest struct {
	Device    domain.Device
	IP        string
	CreatedAt time.Time
	ExpiresAt time.Time
}

type deviceLogin struct {
	userCode  string
	device    domain.Device
	ip        string
	createdAt time.Time
	expiresAt time.Time
	interval  time.Duration
	lastPoll  time.Time
	state     DeviceLoginState
	// login is the session opened on approval, handed over at the next poll.
	login *Login
}

// deviceLogins holds the requests in progress, by hash of the device code and by user code.
type deviceLogins struct {
	mu       sync.Mutex
	byDevice map[string]*deviceLogin
	byUser   map[string]*deviceLogin
}

func hashDeviceCode(code string) string {
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}

// normalizeUserCode puts a typed code in its canonical form: upper case, no dash or space.
func normalizeUserCode(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r - 'a' + 'A'
		case r >= 'A' && r <= 'Z':
			return r
		}
		return -1
	}, s)
}

// displayUserCode shows a code in two groups: "BDWPHQPK" becomes "BDWP-HQPK".
func displayUserCode(code string) string { return code[:4] + "-" + code[4:] }

func newUserCode() string {
	b := make([]byte, userCodeLen)
	_, _ = rand.Read(b) // never returns an error (crypto/rand)
	for i := range b {
		b[i] = userCodeAlphabet[int(b[i])%len(userCodeAlphabet)]
	}
	return string(b)
}

// purgeDeviceLogins forgets expired requests. A session that was approved but never handed over is
// closed (its token is lost). Called with d's lock held.
func (a *App) purgeDeviceLogins(ctx context.Context, d *deviceLogins, now time.Time) {
	for key, l := range d.byDevice {
		if now.Before(l.expiresAt) {
			continue
		}
		delete(d.byDevice, key)
		delete(d.byUser, l.userCode)
		if l.login != nil {
			id := l.login.Session.Session.ID
			if err := a.store.Write(ctx, func(q store.Q) error { return q.DeleteSession(ctx, id) }); err != nil {
				a.log.WarnContext(ctx, "session of a code sign-in never delivered: cannot close it", "session", id, "err", err)
			}
		}
	}
}

// StartDeviceLogin opens a device login request for a device.
func (a *App) StartDeviceLogin(ctx context.Context, dev domain.Device, ip string) (DeviceLoginStart, error) {
	dev = cleanDevice(dev)
	if dev.Name == "" {
		return DeviceLoginStart{}, domain.Invalid("auth.device_name_required")
	}
	now := a.now()
	d := &a.deviceLogins
	d.mu.Lock()
	defer d.mu.Unlock()
	a.purgeDeviceLogins(ctx, d, now)
	fromIP := 0
	for _, l := range d.byDevice {
		if l.ip == ip {
			fromIP++
		}
	}
	if len(d.byDevice) >= maxDeviceLogins || fromIP >= maxDeviceLoginsPerIP {
		return DeviceLoginStart{}, domain.TooManyAttempts("device_login.too_many_pending")
	}
	raw := make([]byte, 32)
	_, _ = rand.Read(raw) // never returns an error (crypto/rand)
	deviceCode := base64.RawURLEncoding.EncodeToString(raw)
	userCode := newUserCode()
	for d.byUser[userCode] != nil {
		userCode = newUserCode()
	}
	l := &deviceLogin{
		userCode: userCode, device: dev, ip: ip, createdAt: now, expiresAt: now.Add(deviceLoginLifetime),
		interval: deviceLoginInterval, state: DeviceLoginPending,
	}
	d.byDevice[hashDeviceCode(deviceCode)] = l
	d.byUser[userCode] = l
	start := DeviceLoginStart{DeviceCode: deviceCode, UserCode: displayUserCode(userCode), ExpiresAt: l.expiresAt, Interval: l.interval}
	start.VerificationURL, start.VerificationURLComplete = a.verificationURLs(start.UserCode)
	return start, nil
}

// PollDeviceLogin returns the state of a request to the device that opened it and, once approved,
// its session (handed over only once). Polling more often than the interval is refused and makes
// the interval longer.
func (a *App) PollDeviceLogin(ctx context.Context, deviceCode string) (DeviceLoginPoll, error) {
	now := a.now()
	d := &a.deviceLogins
	d.mu.Lock()
	defer d.mu.Unlock()
	key := hashDeviceCode(deviceCode)
	l, ok := d.byDevice[key]
	if !ok {
		return DeviceLoginPoll{State: DeviceLoginExpired}, nil
	}
	if !now.Before(l.expiresAt) {
		a.purgeDeviceLogins(ctx, d, now)
		return DeviceLoginPoll{State: DeviceLoginExpired}, nil
	}
	if !l.lastPoll.IsZero() && now.Sub(l.lastPoll) < l.interval {
		l.interval += deviceLoginInterval
		l.lastPoll = now
		return DeviceLoginPoll{}, domain.TooManyAttempts("device_login.slow_down", "interval_seconds", l.interval)
	}
	l.lastPoll = now
	switch l.state {
	case DeviceLoginApproved, DeviceLoginDenied:
		delete(d.byDevice, key)
		delete(d.byUser, l.userCode)
		return DeviceLoginPoll{State: l.state, Login: l.login, Interval: l.interval}, nil
	case DeviceLoginPending, DeviceLoginExpired:
	}
	return DeviceLoginPoll{State: DeviceLoginPending, Interval: l.interval}, nil
}

// pendingDeviceLogin finds a pending request by its user code, for the user approving it. Unknown
// codes count as failed attempts (8 letters out of 20: impossible to guess in a few tries).
func (a *App) pendingDeviceLogin(ctx context.Context, p domain.Principal, userCode string) (*deviceLogin, error) {
	key := "code:" + p.Account.ID.String()
	if wait, ok := a.limiter.Allow(key); !ok {
		return nil, domain.TooManyAttempts("device_login.too_many_wrong_codes", "retry_after_seconds", roundUp(wait))
	}
	now := a.now()
	d := &a.deviceLogins
	a.purgeDeviceLogins(ctx, d, now)
	l, ok := d.byUser[normalizeUserCode(userCode)]
	if !ok || l.state != DeviceLoginPending {
		a.limiter.Fail(key)
		return nil, domain.NotFound("device_login.unknown_code")
	}
	return l, nil
}

// DeviceLogin describes the device waiting behind a code, before it is approved.
func (a *App) DeviceLogin(ctx context.Context, p domain.Principal, userCode string) (DeviceLoginRequest, error) {
	d := &a.deviceLogins
	d.mu.Lock()
	defer d.mu.Unlock()
	l, err := a.pendingDeviceLogin(ctx, p, userCode)
	if err != nil {
		return DeviceLoginRequest{}, err
	}
	return DeviceLoginRequest{Device: l.device, IP: l.ip, CreatedAt: l.createdAt, ExpiresAt: l.expiresAt}, nil
}

// ApproveDeviceLogin signs the device waiting behind a code in to the caller's account. With
// withProfile the device lands on the caller's profile (already unlocked, PIN included); otherwise
// it will pick its profile. A restricted profile (kid, under parental control) cannot approve
// anything.
func (a *App) ApproveDeviceLogin(ctx context.Context, p domain.Principal, userCode string, withProfile bool) error {
	if p.Restricted() {
		return domain.Forbidden("auth.restricted_profile")
	}
	if withProfile && p.Profile == nil {
		return domain.Invalid("device_login.no_profile_to_share")
	}
	d := &a.deviceLogins
	d.mu.Lock()
	defer d.mu.Unlock()
	l, err := a.pendingDeviceLogin(ctx, p, userCode)
	if err != nil {
		return err
	}
	var login Login
	err = a.store.Write(ctx, func(q store.Q) error {
		var err error
		if login, err = a.openSession(ctx, q, p.Account, l.device, l.ip); err != nil {
			return err
		}
		if withProfile {
			if err := q.SetSessionProfile(ctx, login.Session.Session.ID, p.Profile.ID); err != nil {
				return err
			}
			login.Session.Session.ProfileID, login.Session.Profile = &p.Profile.ID, p.Profile
		}
		return nil
	})
	if err != nil {
		return err
	}
	a.limiter.Succeed("code:" + p.Account.ID.String())
	l.state, l.login = DeviceLoginApproved, &login
	a.record(ctx, domain.Activity{
		Kind: domain.ActivityLogin, AccountID: &p.Account.ID,
		Text: domain.T("activity.login_by_code", "username", p.Account.Username, "device", l.device.Name, "client", l.device.Client, "ip", ipOrUnknown(l.ip)),
	})
	return nil
}

// DenyDeviceLogin refuses the request waiting behind a code.
func (a *App) DenyDeviceLogin(ctx context.Context, p domain.Principal, userCode string) error {
	d := &a.deviceLogins
	d.mu.Lock()
	defer d.mu.Unlock()
	l, err := a.pendingDeviceLogin(ctx, p, userCode)
	if err != nil {
		return err
	}
	l.state = DeviceLoginDenied
	return nil
}
