package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
)

// Offline plays: a resume point only replaces an older one. A finished playback counts, once even
// if the report is replayed, and does not wipe a more recent resume point.
func TestSaveOfflineProgress(t *testing.T) {
	st, _ := openTemp(t)
	ctx := context.Background()
	acc := newAccount("family", false)
	prof := domain.Profile{ID: domain.NewID(), AccountID: acc.ID, Name: "Parents", CreatedAt: t0, UpdatedAt: t0}
	lib := newLibrary("Movies", domain.LibraryMovies, "/m")
	movie := domain.Item{ID: domain.NewID(), LibraryID: lib.ID, Kind: domain.ItemMovie, GroupKey: "k", Title: "x", SortTitle: "x", AddedAt: t0, UpdatedAt: t0}
	mustWrite(t, st, func(q Q) error {
		return errors.Join(q.CreateAccount(ctx, acc, "h"), q.CreateProfile(ctx, prof, ""), q.CreateLibrary(ctx, lib), q.CreateItem(ctx, movie))
	})
	save := func(resume time.Duration, finished bool, at time.Time) {
		t.Helper()
		mustWrite(t, st, func(q Q) error {
			_, err := q.SaveOfflineProgress(ctx, prof.ID, movie.ID, resume, finished, at, t0.Add(time.Hour))
			return err
		})
	}
	data := func() domain.UserData {
		t.Helper()
		v, err := st.Read().View(ctx, domain.Viewer{ProfileID: prof.ID}, movie.ID)
		if err != nil {
			t.Fatal(err)
		}
		return v.UserData
	}

	save(20*time.Minute, false, t0.Add(10*time.Minute))
	save(5*time.Minute, false, t0) // older: ignored
	if u := data(); u.Position != 20*time.Minute || !u.LastPlayedAt.Equal(t0.Add(10*time.Minute)) {
		t.Fatalf("most recent resume point: %+v", u)
	}
	// Finished earlier, offline: counted, the more recent resume point stays.
	save(0, true, t0.Add(5*time.Minute))
	save(0, true, t0.Add(5*time.Minute)) // replayed report
	if u := data(); !u.Played || u.PlayCount != 1 || u.Position != 20*time.Minute || !u.LastPlayedAt.Equal(t0.Add(10*time.Minute)) {
		t.Fatalf("finished earlier: %+v", u)
	}
	// Finished later: counted, no resume point anymore.
	save(0, true, t0.Add(30*time.Minute))
	if u := data(); u.PlayCount != 2 || u.Position != 0 || !u.LastPlayedAt.Equal(t0.Add(30*time.Minute)) {
		t.Fatalf("finished later: %+v", u)
	}
}
