package store

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
)

var t0 = time.Date(2026, 9, 28, 20, 0, 0, 0, time.UTC)

func mustWrite(t *testing.T, st *Store, fn func(q Q) error) {
	t.Helper()
	if err := st.Write(context.Background(), fn); err != nil {
		t.Fatal(err)
	}
}

func newAccount(name string, admin bool) domain.Account {
	return domain.Account{ID: domain.NewID(), Username: name, IsAdmin: admin, Libraries: domain.AllLibraries(), CreatedAt: t0, UpdatedAt: t0}
}

func TestAccountsUniqueIgnoringCase(t *testing.T) {
	st, _ := openTemp(t)
	ctx := context.Background()
	chloe := newAccount("Chloé", true)
	mustWrite(t, st, func(q Q) error { return q.CreateAccount(ctx, chloe, "hash") })

	err := st.Write(ctx, func(q Q) error { return q.CreateAccount(ctx, newAccount(" cHLOÉ ", false), "x") })
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("name already taken (different case): %v, want ErrDuplicate", err)
	}

	got, hash, err := st.Read().AccountByUsername(ctx, "CHLOÉ")
	if err != nil || got.ID != chloe.ID || !got.IsAdmin || hash != "hash" || !got.CreatedAt.Equal(t0) {
		t.Fatalf("account read back %+v %q %v", got, hash, err)
	}
	if n, _ := st.Read().CountAccounts(ctx); n != 1 {
		t.Errorf("%d accounts", n)
	}
}

func TestProfilesAndSessionsLifecycle(t *testing.T) {
	st, _ := openTemp(t)
	ctx := context.Background()
	acc := newAccount("famille", false)
	adult := domain.Profile{ID: domain.NewID(), AccountID: acc.ID, Name: "Parents", CreatedAt: t0, UpdatedAt: t0}
	kid := domain.Profile{ID: domain.NewID(), AccountID: acc.ID, Name: "Enfants", Kid: true, CreatedAt: t0.Add(time.Second), UpdatedAt: t0}
	session := domain.Session{
		ID: domain.NewID(), AccountID: acc.ID, Device: domain.Device{Name: "Salon", Client: "Test", ClientVersion: "1", Platform: "TV"},
		CreatedAt: t0, LastUsedAt: t0, ExpiresAt: t0.Add(time.Hour),
	}
	mustWrite(t, st, func(q Q) error {
		if err := q.CreateAccount(ctx, acc, "h"); err != nil {
			return err
		}
		if err := q.CreateProfile(ctx, adult, "pinhash"); err != nil {
			return err
		}
		if err := q.CreateProfile(ctx, kid, ""); err != nil {
			return err
		}
		if err := q.CreateSession(ctx, session, "tokenhash"); err != nil {
			return err
		}
		return q.SetSessionProfile(ctx, session.ID, kid.ID)
	})

	profiles, err := st.Read().Profiles(ctx, acc.ID)
	if err != nil || len(profiles) != 2 || profiles[0].ID != adult.ID || !profiles[0].HasPIN || !profiles[1].Kid {
		t.Fatalf("profiles: %+v %v", profiles, err)
	}
	dup := domain.Profile{ID: domain.NewID(), AccountID: acc.ID, Name: "PARENTS", CreatedAt: t0, UpdatedAt: t0}
	if err := st.Write(ctx, func(q Q) error { return q.CreateProfile(ctx, dup, "") }); !errors.Is(err, ErrDuplicate) {
		t.Errorf("duplicate profile name: %v", err)
	}

	got, err := st.Read().SessionByTokenHash(ctx, "tokenhash")
	if err != nil || got.ProfileID == nil || *got.ProfileID != kid.ID || got.Device.Platform != "TV" {
		t.Fatalf("session: %+v %v", got, err)
	}

	// Deleting the picked profile sends the session back to the profile picker.
	mustWrite(t, st, func(q Q) error { return q.DeleteProfile(ctx, kid.ID) })
	if got, _ = st.Read().Session(ctx, session.ID); got.ProfileID != nil {
		t.Errorf("deleted profile still picked: %v", got.ProfileID)
	}

	// Valid sessions only, then expired ones are purged.
	if list, _ := st.Read().Sessions(ctx, acc.ID, t0.Add(2*time.Hour)); len(list) != 0 {
		t.Errorf("expired session listed: %+v", list)
	}
	var purged int64
	mustWrite(t, st, func(q Q) error {
		var err error
		purged, err = q.DeleteExpiredSessions(ctx, t0.Add(2*time.Hour))
		return err
	})
	if purged != 1 {
		t.Errorf("%d sessions purged", purged)
	}
}

func TestDeletingAccountCascades(t *testing.T) {
	st, _ := openTemp(t)
	ctx := context.Background()
	acc := newAccount("temporaire", false)
	p := domain.Profile{ID: domain.NewID(), AccountID: acc.ID, Name: "p", CreatedAt: t0, UpdatedAt: t0}
	mustWrite(t, st, func(q Q) error {
		if err := q.CreateAccount(ctx, acc, "h"); err != nil {
			return err
		}
		return q.CreateProfile(ctx, p, "")
	})
	if _, err := st.writer.ExecContext(ctx, "DELETE FROM accounts WHERE id = ?", acc.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.Read().Profile(ctx, p.ID); !IsNotFound(err) {
		t.Errorf("orphan profile (foreign keys off?): %v", err)
	}
}

func TestAccountAccessAndSummaries(t *testing.T) {
	st, _ := openTemp(t)
	ctx := context.Background()
	films, series := newLibrary("Films", domain.LibraryMovies, "/m"), newLibrary("Séries", domain.LibraryShows, "/s")
	admin := newAccount("Chloé", true)
	twelve := 12
	kid := newAccount("Léa", false)
	kid.Libraries = domain.LibraryAccess{IDs: []domain.ID{films.ID}}
	kid.Parental = domain.ParentalControl{MaxAge: &twelve, BlockUnrated: true}
	profile := domain.Profile{ID: domain.NewID(), AccountID: kid.ID, Name: "Léa", Parental: domain.KidParentalControl(), Kid: true, CreatedAt: t0, UpdatedAt: t0}
	session := domain.Session{ID: domain.NewID(), AccountID: kid.ID, Device: domain.Device{Name: "TV"}, CreatedAt: t0, LastUsedAt: t0.Add(time.Hour), ExpiresAt: t0.Add(48 * time.Hour)}
	mustWrite(t, st, func(q Q) error {
		return errors.Join(q.CreateLibrary(ctx, films), q.CreateLibrary(ctx, series), q.CreateAccount(ctx, admin, "h"),
			q.CreateAccount(ctx, kid, "h"), q.CreateProfile(ctx, profile, ""), q.CreateSession(ctx, session, "jeton"))
	})
	got, err := st.Read().Account(ctx, kid.ID)
	if err != nil || got.Libraries.All || !slices.Equal(got.Libraries.IDs, []domain.ID{films.ID}) ||
		got.Parental.MaxAge == nil || *got.Parental.MaxAge != 12 || !got.Parental.BlockUnrated {
		t.Fatalf("account read back: %+v %v", got, err)
	}
	if p, _, _ := st.Read().Profile(ctx, profile.ID); p.Parental.MaxAge == nil || *p.Parental.MaxAge != domain.KidMaxAge || !p.Parental.BlockUnrated {
		t.Errorf("profile read back: %+v", p)
	}
	sums, err := st.Read().AccountSummaries(ctx)
	if err != nil || len(sums) != 2 || sums[1].Profiles != 1 || sums[1].LastActive == nil || !sums[1].LastActive.Equal(t0.Add(time.Hour)) ||
		sums[0].LastActive != nil || !sums[0].Account.Libraries.All || len(sums[1].Account.Libraries.IDs) != 1 {
		t.Fatalf("summaries: %+v %v", sums, err)
	}

	// All libraries, disabled. Enabled administrators are counted.
	got.Libraries, got.Disabled, got.Parental = domain.AllLibraries(), true, domain.ParentalControl{}
	mustWrite(t, st, func(q Q) error { return errors.Join(q.UpdateAccount(ctx, got), q.DeleteAccountSessions(ctx, kid.ID)) })
	if again, _ := st.Read().Account(ctx, kid.ID); !again.Libraries.All || again.Libraries.IDs != nil || !again.Disabled || again.Parental.Active() {
		t.Errorf("after the update: %+v", again)
	}
	if n, _ := st.Read().CountEnabledAdmins(ctx); n != 1 {
		t.Errorf("%d enabled administrators", n)
	}
	if _, err := st.Read().Session(ctx, session.ID); !IsNotFound(err) {
		t.Errorf("session still there: %v", err)
	}
	// A deleted library leaves the access list, and a deleted account takes its profiles with it.
	got.Libraries = domain.LibraryAccess{IDs: []domain.ID{films.ID, series.ID}}
	mustWrite(t, st, func(q Q) error { return errors.Join(q.UpdateAccount(ctx, got), q.DeleteLibrary(ctx, series.ID)) })
	if again, _ := st.Read().Account(ctx, kid.ID); !slices.Equal(again.Libraries.IDs, []domain.ID{films.ID}) {
		t.Errorf("deleted library: %v", again.Libraries.IDs)
	}
	mustWrite(t, st, func(q Q) error { return q.DeleteAccount(ctx, kid.ID) })
	if _, _, err := st.Read().Profile(ctx, profile.ID); !IsNotFound(err) {
		t.Errorf("profile still there: %v", err)
	}
}
