package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
)

// historyFixture creates a profile, a library and a movie.
func historyFixture(tb testing.TB, st *Store) (domain.Profile, domain.Item) {
	tb.Helper()
	ctx := context.Background()
	acc := newAccount("family", false)
	prof := domain.Profile{ID: domain.NewID(), AccountID: acc.ID, Name: "Parents", CreatedAt: t0, UpdatedAt: t0}
	lib := newLibrary("Movies", domain.LibraryMovies, "/m")
	movie := domain.Item{ID: domain.NewID(), LibraryID: lib.ID, Kind: domain.ItemMovie, GroupKey: "k", Title: "Movie", SortTitle: "movie", AddedAt: t0, UpdatedAt: t0}
	if err := st.Write(ctx, func(q Q) error {
		return errors.Join(q.CreateAccount(ctx, acc, "h"), q.CreateProfile(ctx, prof, ""), q.CreateLibrary(ctx, lib), q.CreateItem(ctx, movie))
	}); err != nil {
		tb.Fatal(err)
	}
	return prof, movie
}

func play(profile domain.ID, item domain.ID, at time.Time) domain.Play {
	return domain.Play{
		ID: domain.NewID(), ProfileID: profile, ItemID: &item, Kind: domain.ItemMovie, Title: "Movie",
		StartedAt: at, EndedAt: at.Add(time.Hour), Watched: time.Hour, Duration: 2 * time.Hour, Device: "TV",
	}
}

// History: cursor pages, period, deletion limited to the profile. A play outlives a forgotten item.
func TestPlayHistory(t *testing.T) {
	st, _ := openTemp(t)
	ctx := context.Background()
	prof, movie := historyFixture(t, st)
	plays := []domain.Play{play(prof.ID, movie.ID, t0), play(prof.ID, movie.ID, t0.Add(24*time.Hour)), play(prof.ID, movie.ID, t0.Add(48*time.Hour))}
	mustWrite(t, st, func(q Q) error {
		for _, p := range plays {
			if err := q.AddPlay(ctx, p); err != nil {
				return err
			}
		}
		return nil
	})
	read := st.Read()
	first, err := read.Plays(ctx, prof.ID, PlayCursor{}, 2)
	if err != nil || len(first) != 2 || first[0].ID != plays[2].ID || first[1].ID != plays[1].ID {
		t.Fatalf("first page: %+v %v", first, err)
	}
	rest, err := read.Plays(ctx, prof.ID, PlayCursor{At: first[1].StartedAt, ID: first[1].ID}, 2)
	if err != nil || len(rest) != 1 || rest[0].ID != plays[0].ID || rest[0].Watched != time.Hour || rest[0].Device != "TV" {
		t.Fatalf("next page: %+v %v", rest, err)
	}
	collect := func(from, to time.Time) []domain.Play {
		t.Helper()
		var out []domain.Play
		if err := read.EachPlay(ctx, prof.ID, from, to, func(p *domain.Play) { out = append(out, *p) }); err != nil {
			t.Fatal(err)
		}
		return out
	}
	if between := collect(t0.Add(time.Hour), t0.Add(48*time.Hour)); len(between) != 1 || !between[0].StartedAt.Equal(plays[1].StartedAt) ||
		between[0].Watched != time.Hour || *between[0].ItemID != movie.ID {
		t.Errorf("period: %+v", between)
	}
	fp, lp, err := read.FirstLastPlays(ctx, prof.ID, time.Time{}, time.Time{})
	if err != nil || fp == nil || lp == nil || fp.ID != plays[0].ID || lp.ID != plays[2].ID || lp.Device != "TV" {
		t.Errorf("first and last: %+v %+v %v", fp, lp, err)
	}
	if first, last, err := read.FirstLastPlays(ctx, domain.NewID(), time.Time{}, time.Time{}); first != nil || last != nil || err != nil {
		t.Errorf("profile with no play: %v %v %v", first, last, err)
	}

	mustWrite(t, st, func(q Q) error {
		if ok, err := q.DeletePlay(ctx, domain.NewID(), plays[0].ID); err != nil || ok {
			t.Errorf("play deleted by another profile: %v %v", ok, err)
		}
		if ok, err := q.DeletePlay(ctx, prof.ID, plays[0].ID); err != nil || !ok {
			t.Errorf("play not deleted: %v %v", ok, err)
		}
		_, err := q.DeleteOrphanItems(ctx, movie.LibraryID) // the movie has no file: forgotten
		return err
	})
	if all := collect(time.Time{}, time.Time{}); len(all) != 2 || all[0].ItemID != nil || all[0].Title != "Movie" {
		t.Errorf("forgotten item: %+v", all)
	}
	mustWrite(t, st, func(q Q) error {
		n, err := q.ClearPlays(ctx, prof.ID)
		if n != 2 {
			t.Errorf("deleted: %d", n)
		}
		return err
	})
}

// An extreme year: 20,000 plays by one profile, walked for its statistics.
func BenchmarkEachPlay20000(b *testing.B) {
	st, err := Open(context.Background(), b.TempDir()+"/bench.db")
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()
	prof, movie := historyFixture(b, st)
	if err := st.Write(ctx, func(q Q) error {
		for i := range 20000 {
			if err := q.AddPlay(ctx, play(prof.ID, movie.ID, t0.Add(time.Duration(i)*26*time.Minute))); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		b.Fatal(err)
	}
	from, to := t0, t0.Add(366*24*time.Hour)
	for b.Loop() {
		n := 0
		if err := st.Read().EachPlay(ctx, prof.ID, from, to, func(*domain.Play) { n++ }); err != nil || n != 20000 {
			b.Fatal(n, err)
		}
	}
}
