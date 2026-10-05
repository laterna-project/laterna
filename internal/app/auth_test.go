package app

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/store"
)

// clock is a test clock that is safe across goroutines (background work reads it too).
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newClock() *clock { return &clock{t: time.Date(2026, 9, 28, 20, 0, 0, 0, time.UTC)} }

func dev(name string) domain.Device {
	return domain.Device{Name: name, Client: "Tests", ClientVersion: "1.0", Platform: "Go"}
}
func isKind(err error, kind error) bool { return errors.Is(err, kind) }
func mustNil(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func newTestApp(t *testing.T) (*App, *clock) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	a, err := New(ctx, st, Options{ServerName: "Test"})
	if err != nil {
		t.Fatal(err)
	}
	c := newClock()
	a.now = c.now
	a.limiter.SetClock(c.now)
	return a, c
}

// setupAdmin sets the server up and returns the matching principal.
func setupAdmin(t *testing.T, a *App) (Login, domain.Principal) {
	t.Helper()
	ctx := context.Background()
	login, err := a.Setup(ctx, "Chloé", "a-strong-password", dev("PC"), "10.0.0.1")
	mustNil(t, err)
	p, err := a.Authenticate(ctx, login.Token, "10.0.0.1")
	mustNil(t, err)
	return login, p
}

func TestSetupOnlyOnce(t *testing.T) {
	a, _ := newTestApp(t)
	ctx := context.Background()
	if required, _ := a.SetupRequired(ctx); !required {
		t.Fatal("want setup required on an empty database")
	}
	if _, err := a.Setup(ctx, "admin", "short", dev("PC"), ""); !isKind(err, domain.ErrInvalid) {
		t.Errorf("password too short accepted: %v", err)
	}
	login, p := setupAdmin(t, a)
	if !p.Account.IsAdmin || p.Profile == nil || p.Profile.Name != "Chloé" {
		t.Errorf("after setup: admin=%v profile=%+v", p.Account.IsAdmin, p.Profile)
	}
	if login.Session.Profile == nil {
		t.Error("the only profile, without a PIN, must be picked automatically")
	}
	if required, _ := a.SetupRequired(ctx); required {
		t.Error("setup still required")
	}
	if _, err := a.Setup(ctx, "other", "a-strong-password", dev("PC"), ""); !isKind(err, domain.ErrPrecondition) {
		t.Errorf("second setup: %v", err)
	}
}

func TestLoginAndRateLimit(t *testing.T) {
	a, c := newTestApp(t)
	ctx := context.Background()
	setupAdmin(t, a)

	if _, err := a.Login(ctx, "CHLOÉ", "a-strong-password", dev("TV"), "10.0.0.2"); err != nil {
		t.Fatalf("login (name in a different case): %v", err)
	}
	if _, err := a.Login(ctx, "unknown", "a-strong-password", dev("TV"), "10.0.0.3"); !isKind(err, domain.ErrUnauthenticated) {
		t.Errorf("unknown account: %v", err)
	}
	for range 5 {
		if _, err := a.Login(ctx, "chloé", "wrong", dev("TV"), "10.0.0.2"); !isKind(err, domain.ErrUnauthenticated) {
			t.Fatalf("wrong password: %v", err)
		}
	}
	if _, err := a.Login(ctx, "chloé", "a-strong-password", dev("TV"), "10.0.0.2"); !isKind(err, domain.ErrTooManyAttempts) {
		t.Fatalf("after 5 failures even the right password must wait: %v", err)
	}
	c.advance(16 * time.Minute)
	if _, err := a.Login(ctx, "chloé", "a-strong-password", dev("TV"), "10.0.0.2"); err != nil {
		t.Fatalf("after waiting: %v", err)
	}
}

func TestSessionSlidingExpiryAndLogout(t *testing.T) {
	a, c := newTestApp(t)
	ctx := context.Background()
	login, _ := setupAdmin(t, a)

	// Used every 60 days, the session never dies.
	for range 3 {
		c.advance(60 * 24 * time.Hour)
		if _, err := a.Authenticate(ctx, login.Token, "10.0.0.1"); err != nil {
			t.Fatalf("session extended by use: %v", err)
		}
	}
	// Left unused for 91 days, it expires.
	c.advance(91 * 24 * time.Hour)
	if _, err := a.Authenticate(ctx, login.Token, "10.0.0.1"); !isKind(err, domain.ErrUnauthenticated) {
		t.Fatalf("unused session: %v", err)
	}
	if n, err := a.PurgeExpiredSessions(ctx); err != nil || n != 1 {
		t.Errorf("purge: %d, %v", n, err)
	}

	again, err := a.Login(ctx, "chloé", "a-strong-password", dev("PC"), "")
	mustNil(t, err)
	p, err := a.Authenticate(ctx, again.Token, "")
	mustNil(t, err)
	mustNil(t, a.Logout(ctx, p))
	if _, err := a.Authenticate(ctx, again.Token, ""); !isKind(err, domain.ErrUnauthenticated) {
		t.Errorf("token still valid after logout: %v", err)
	}
	for _, bad := range []string{"", "lat_unknown", "anything at all"} {
		if _, err := a.Authenticate(ctx, bad, ""); !isKind(err, domain.ErrUnauthenticated) {
			t.Errorf("token %q: %v", bad, err)
		}
	}
}

func TestSessionsRevokeAndChangePassword(t *testing.T) {
	a, _ := newTestApp(t)
	ctx := context.Background()
	_, pc := setupAdmin(t, a)
	tv, err := a.Login(ctx, "chloé", "a-strong-password", dev("Living room TV"), "10.0.0.5")
	mustNil(t, err)

	sessions, err := a.Sessions(ctx, pc)
	mustNil(t, err)
	if len(sessions) != 2 || sessions[0].Profile == nil {
		t.Fatalf("sessions: %+v", sessions)
	}

	mustNil(t, a.RevokeSession(ctx, pc, tv.Session.Session.ID))
	if _, err := a.Authenticate(ctx, tv.Token, ""); !isKind(err, domain.ErrUnauthenticated) {
		t.Errorf("revoked device still signed in: %v", err)
	}
	if err := a.RevokeSession(ctx, pc, domain.NewID()); !isKind(err, domain.ErrNotFound) {
		t.Errorf("unknown session: %v", err)
	}

	phone, err := a.Login(ctx, "chloé", "a-strong-password", dev("Phone"), "")
	mustNil(t, err)
	if err := a.ChangePassword(ctx, pc, "wrong", "a-new-password"); !isKind(err, domain.ErrUnauthenticated) {
		t.Errorf("wrong current password: %v", err)
	}
	mustNil(t, a.ChangePassword(ctx, pc, "a-strong-password", "a-new-password"))
	if _, err := a.Authenticate(ctx, phone.Token, ""); !isKind(err, domain.ErrUnauthenticated) {
		t.Error("other devices must be signed out after a password change")
	}
	if _, err := a.Authenticate(ctx, "", ""); err == nil {
		t.Error("empty token accepted")
	}
	if _, err := a.Login(ctx, "chloé", "a-new-password", dev("PC"), ""); err != nil {
		t.Errorf("login with the new password: %v", err)
	}
}

func TestProfilesRules(t *testing.T) {
	a, _ := newTestApp(t)
	ctx := context.Background()
	_, adult := setupAdmin(t, a)

	kid, err := a.CreateProfile(ctx, adult, "Lou", "", true, nil, "")
	mustNil(t, err)
	locked, err := a.CreateProfile(ctx, adult, "Parents", "1234", false, nil, "")
	mustNil(t, err)
	if _, err := a.CreateProfile(ctx, adult, "  lou ", "", false, nil, ""); !isKind(err, domain.ErrConflict) {
		t.Errorf("duplicate name: %v", err)
	}
	if _, err := a.CreateProfile(ctx, adult, "X", "12a4", false, nil, ""); !isKind(err, domain.ErrInvalid) {
		t.Errorf("non-numeric PIN: %v", err)
	}

	// Picking a protected profile requires its PIN.
	if _, err := a.SelectProfile(ctx, adult, locked.ID, ""); !isKind(err, domain.ErrForbidden) {
		t.Errorf("without a PIN: %v", err)
	}
	if _, err := a.SelectProfile(ctx, adult, locked.ID, "0000"); !isKind(err, domain.ErrForbidden) {
		t.Errorf("wrong PIN: %v", err)
	}
	if _, err := a.SelectProfile(ctx, adult, locked.ID, "1234"); err != nil {
		t.Errorf("right PIN: %v", err)
	}

	// A kid profile manages nothing.
	_, err = a.SelectProfile(ctx, adult, kid.ID, "")
	mustNil(t, err)
	child := adult
	child.Profile = &kid
	if _, err := a.CreateProfile(ctx, child, "Pirate", "", false, nil, ""); !isKind(err, domain.ErrForbidden) {
		t.Errorf("kid creating a profile: %v", err)
	}
	if _, err := a.UpdateProfile(ctx, child, locked.ID, "", ProfileChanges{PIN: new("")}); !isKind(err, domain.ErrForbidden) {
		t.Errorf("kid removing a PIN: %v", err)
	}

	// Changing a protected profile requires its PIN.
	if _, err := a.UpdateProfile(ctx, adult, locked.ID, "", ProfileChanges{Name: new("Dad")}); !isKind(err, domain.ErrForbidden) {
		t.Errorf("change without the PIN: %v", err)
	}
	renamed, err := a.UpdateProfile(ctx, adult, locked.ID, "1234", ProfileChanges{Name: new("Dad"), PIN: new("")})
	mustNil(t, err)
	if renamed.Name != "Dad" || renamed.HasPIN {
		t.Errorf("changed profile: %+v", renamed)
	}

	// There is always an adult profile left.
	first := *adult.Profile
	mustNil(t, a.DeleteProfile(ctx, adult, renamed.ID, ""))
	if err := a.DeleteProfile(ctx, adult, first.ID, ""); !isKind(err, domain.ErrPrecondition) {
		t.Errorf("deleting the last adult profile: %v", err)
	}
	if _, err := a.UpdateProfile(ctx, adult, first.ID, "", ProfileChanges{Kid: new(true)}); !isKind(err, domain.ErrPrecondition) {
		t.Errorf("last adult profile turned into a kid: %v", err)
	}

	// Profiles of another account cannot be found.
	if _, err := a.SelectProfile(ctx, adult, domain.NewID(), ""); !isKind(err, domain.ErrNotFound) {
		t.Errorf("unknown profile: %v", err)
	}
}

func TestLoginDoesNotAutoSelectAmongSeveralProfiles(t *testing.T) {
	a, _ := newTestApp(t)
	ctx := context.Background()
	_, adult := setupAdmin(t, a)
	_, err := a.CreateProfile(ctx, adult, "Lou", "", true, nil, "")
	mustNil(t, err)
	login, err := a.Login(ctx, "chloé", "a-strong-password", dev("TV"), "")
	mustNil(t, err)
	if login.Session.Profile != nil {
		t.Error("with several profiles the choice is the user's")
	}
}
