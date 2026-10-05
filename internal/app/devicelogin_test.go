package app

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
)

// Device login: the TV asks for a code, the user approves it from their phone, the TV gets its
// session (only once).
func TestDeviceLogin(t *testing.T) {
	a, c := newTestApp(t)
	_, phone := setupAdmin(t, a)
	ctx := context.Background()

	start, err := a.StartDeviceLogin(ctx, dev("Living room"), "10.0.0.9")
	mustNil(t, err)
	if !regexp.MustCompile(`^[BCDFGHJKLMNPQRSTVWXZ]{4}-[BCDFGHJKLMNPQRSTVWXZ]{4}$`).MatchString(start.UserCode) ||
		len(start.DeviceCode) < 40 || start.Interval != 5*time.Second || !start.ExpiresAt.Equal(c.now().Add(10*time.Minute)) {
		t.Fatalf("request: %+v", start)
	}
	// With no address set the server does not know where codes are approved: the TV shows the code
	// alone.
	if start.VerificationURL != "" || start.VerificationURLComplete != "" {
		t.Errorf("verification URL without any address set: %+v", start)
	}
	poll, err := a.PollDeviceLogin(ctx, start.DeviceCode)
	mustNil(t, err)
	if poll.State != DeviceLoginPending || poll.Login != nil {
		t.Fatalf("pending: %+v", poll)
	}
	// Too soon: refused, and the interval grows.
	if _, err := a.PollDeviceLogin(ctx, start.DeviceCode); !isKind(err, domain.ErrTooManyAttempts) {
		t.Errorf("poll too soon: %v", err)
	}
	c.advance(11 * time.Second)
	if poll, err = a.PollDeviceLogin(ctx, start.DeviceCode); err != nil || poll.Interval != 10*time.Second {
		t.Errorf("longer interval: %+v %v", poll, err)
	}

	// The phone sees the device that is asking (code typed in lower case, without the dash).
	typed := strings.ToLower(strings.ReplaceAll(start.UserCode, "-", " "))
	req, err := a.DeviceLogin(ctx, phone, typed)
	mustNil(t, err)
	if req.Device.Name != "Living room" || req.IP != "10.0.0.9" {
		t.Errorf("device: %+v", req)
	}
	if _, err := a.DeviceLogin(ctx, phone, "BBBB-BBBB"); !isKind(err, domain.ErrNotFound) {
		t.Errorf("unknown code: %v", err)
	}

	// A kid profile cannot approve.
	kid, err := a.CreateProfile(ctx, phone, "Kid", "", true, nil, "")
	mustNil(t, err)
	kidP := phone
	kidP.Profile = &kid
	if err := a.ApproveDeviceLogin(ctx, kidP, start.UserCode, false); !isKind(err, domain.ErrForbidden) {
		t.Errorf("approval by a kid: %v", err)
	}

	// Approved with the phone's profile: the TV gets a session already on that profile.
	mustNil(t, a.ApproveDeviceLogin(ctx, phone, typed, true))
	c.advance(11 * time.Second)
	poll, err = a.PollDeviceLogin(ctx, start.DeviceCode)
	mustNil(t, err)
	if poll.State != DeviceLoginApproved || poll.Login == nil || poll.Login.Token == "" {
		t.Fatalf("approved: %+v", poll)
	}
	tv, err := a.Authenticate(ctx, poll.Login.Token, "10.0.0.9")
	mustNil(t, err)
	if tv.Profile == nil || tv.Profile.ID != phone.Profile.ID || tv.SessionID == phone.SessionID {
		t.Errorf("TV session: %+v", tv)
	}
	// Handed over only once.
	c.advance(11 * time.Second)
	if again, err := a.PollDeviceLogin(ctx, start.DeviceCode); err != nil || again.State != DeviceLoginExpired || again.Login != nil {
		t.Errorf("second handover: %+v %v", again, err)
	}

	// Denied.
	other, err := a.StartDeviceLogin(ctx, dev("Bedroom"), "10.0.0.10")
	mustNil(t, err)
	mustNil(t, a.DenyDeviceLogin(ctx, phone, other.UserCode))
	if poll, err := a.PollDeviceLogin(ctx, other.DeviceCode); err != nil || poll.State != DeviceLoginDenied {
		t.Errorf("denied: %+v %v", poll, err)
	}

	// Approved but never collected: the session is closed when the request expires.
	lost, err := a.StartDeviceLogin(ctx, dev("Attic"), "10.0.0.11")
	mustNil(t, err)
	mustNil(t, a.ApproveDeviceLogin(ctx, phone, lost.UserCode, false))
	before, err := a.Sessions(ctx, phone)
	mustNil(t, err)
	c.advance(11 * time.Minute)
	if poll, err := a.PollDeviceLogin(ctx, lost.DeviceCode); err != nil || poll.State != DeviceLoginExpired {
		t.Errorf("expired: %+v %v", poll, err)
	}
	after, err := a.Sessions(ctx, phone)
	mustNil(t, err)
	if len(after) != len(before)-1 {
		t.Errorf("session never handed over still open: %d then %d", len(before), len(after))
	}

	// Wrong codes over and over: blocked.
	for range 5 {
		_, _ = a.DeviceLogin(ctx, phone, "ZZZZ-ZZZZ")
	}
	if _, err := a.DeviceLogin(ctx, phone, "ZZZZ-ZZZZ"); !isKind(err, domain.ErrTooManyAttempts) {
		t.Errorf("repeated attempts: %v", err)
	}
}

// The URL where a code is approved is built by the server under the web client's address, otherwise
// under its own. The TV does not need to know the web client's routes.
func TestDeviceLoginVerificationURL(t *testing.T) {
	a, _ := newTestApp(t)
	_, admin := setupAdmin(t, a)
	ctx := context.Background()
	start := func() DeviceLoginStart {
		t.Helper()
		s, err := a.StartDeviceLogin(ctx, dev("Living room"), "10.0.0.9")
		mustNil(t, err)
		return s
	}
	set := func(ch SettingsChanges) {
		t.Helper()
		_, err := a.UpdateSettings(ctx, admin, ch)
		mustNil(t, err)
	}

	// Web client served from the same address as the server (under a path, behind a proxy).
	public := "https://media.example.org/laterna/"
	set(SettingsChanges{PublicURL: &public})
	s := start()
	if s.VerificationURL != "https://media.example.org/laterna/device" ||
		s.VerificationURLComplete != "https://media.example.org/laterna/device?code="+s.UserCode {
		t.Errorf("under the server's address: %q %q", s.VerificationURL, s.VerificationURLComplete)
	}

	// Web client hosted elsewhere: its address wins.
	web := " https://app.example.org/ "
	set(SettingsChanges{WebURL: &web})
	if got := a.Settings().WebURL; got != "https://app.example.org" {
		t.Errorf("web client address: %q", got)
	}
	s = start()
	if s.VerificationURL != "https://app.example.org/device" || s.VerificationURLComplete != "https://app.example.org/device?code="+s.UserCode {
		t.Errorf("under the web client's address: %q %q", s.VerificationURL, s.VerificationURLComplete)
	}

	// Removed: back to the server's address.
	none := ""
	set(SettingsChanges{WebURL: &none})
	if s = start(); s.VerificationURL != "https://media.example.org/laterna/device" {
		t.Errorf("web client address removed: %q", s.VerificationURL)
	}
	for _, bad := range []string{"app.example.org", "ftp://app.example.org", "https://app.example.org/?x=1", "https://user@app.example.org"} {
		if _, err := a.UpdateSettings(ctx, admin, SettingsChanges{WebURL: &bad}); domain.CodeOf(err) != "settings.invalid_web_url" {
			t.Errorf("address %q: %v", bad, err)
		}
	}
}
