package app

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/store"
)

func rowKinds(rows []HomeRow) []HomeRowKind {
	out := make([]HomeRowKind, len(rows))
	for i, r := range rows {
		out[i] = r.Kind
	}
	return out
}

func TestHome(t *testing.T) {
	a, p, films, shows := catalogApp(t)
	ctx := context.Background()

	rows, err := a.Home(ctx, p, 0)
	mustNil(t, err)
	// Nothing started: only the recently added rows, libraries by name.
	if len(rows) != 2 || rows[0].Kind != RowLatestMovies || rows[0].Library.ID != films.ID || len(rows[0].Items) != 5 ||
		rows[1].Kind != RowLatestSeries || rows[1].Library.ID != shows.ID || rows[1].Title.String() != "home.latest (library=Shows)" {
		t.Fatalf("initial home: %v", rowKinds(rows))
	}

	// One episode played: the next one is "next up".
	eps, err := a.Episodes(ctx, p, rows[1].Items[0].Item.ID, nil)
	mustNil(t, err)
	if len(eps) != 4 {
		t.Fatalf("%d episodes", len(eps))
	}
	mustNil(t, a.SetPlayed(ctx, p, eps[0].Item.ID, true))
	// A started movie (the position is written directly: fixture movies are under a minute long,
	// too short for a resume point).
	movie := rows[0].Items[0].Item
	mustNil(t, a.store.Write(ctx, func(q store.Q) error {
		return q.SaveProgress(ctx, p.Profile.ID, movie.ID, 30*time.Second, false, a.now())
	}))
	rows, err = a.Home(ctx, p, 3)
	mustNil(t, err)
	// What was watched also yields recommendations.
	if len(rows) != 5 || !slices.Equal(rowKinds(rows), []HomeRowKind{RowResume, RowNextUp, RowLatestMovies, RowLatestSeries, RowRecommended}) ||
		len(rows[0].Items) == 0 || len(rows[1].Items) == 0 || rows[0].Items[0].Item.ID != movie.ID || rows[1].Items[0].Item.ID != eps[1].Item.ID || len(rows[2].Items) != 3 {
		t.Fatalf("home after watching: %v", rowKinds(rows))
	}

	// The next episode, once started, moves to "Resume" and is not listed again in "Next up".
	mustNil(t, a.store.Write(ctx, func(q store.Q) error {
		return q.SaveProgress(ctx, p.Profile.ID, eps[1].Item.ID, 10*time.Second, false, a.now())
	}))
	rows, err = a.Home(ctx, p, 0)
	mustNil(t, err)
	if len(rows) < 2 || rows[1].Kind == RowNextUp {
		t.Errorf("started episode listed twice, also in \"Next up\"")
	}
	if _, err := a.Home(ctx, p, 51); !isKind(err, domain.ErrInvalid) {
		t.Errorf("rows too long: %v", err)
	}
}

// Library order: chosen by the administrator, followed by lists and by the recently added rows of
// the home page.
func TestReorderLibraries(t *testing.T) {
	a, p, films, shows := catalogApp(t)
	ctx := context.Background()
	libraryNames := func() []string {
		t.Helper()
		libs, err := a.CatalogLibraries(ctx, p)
		mustNil(t, err)
		out := make([]string, len(libs))
		for i, l := range libs {
			out[i] = l.Library.Name
		}
		return out
	}
	if got := libraryNames(); !slices.Equal(got, []string{"Movies", "Shows"}) {
		t.Fatalf("default order: %v", got)
	}

	// An order must name every library once, and only once.
	for name, ids := range map[string][]domain.ID{
		"incomplete": {shows.ID}, "twice": {shows.ID, shows.ID}, "unknown": {shows.ID, domain.NewID()},
		"one too many": {shows.ID, films.ID, domain.NewID()}, "empty": nil,
	} {
		if _, err := a.ReorderLibraries(ctx, ids); domain.CodeOf(err) != "library.invalid_order" {
			t.Errorf("order %s: %v", name, err)
		}
	}

	list, err := a.ReorderLibraries(ctx, []domain.ID{shows.ID, films.ID})
	mustNil(t, err)
	if len(list) != 2 || list[0].Library.ID != shows.ID || list[0].Library.Position != 1 || list[1].Library.Position != 2 {
		t.Fatalf("after reordering: %+v", list)
	}
	if got := libraryNames(); !slices.Equal(got, []string{"Shows", "Movies"}) {
		t.Errorf("lists: %v", got)
	}
	rows, err := a.Home(ctx, p, 0)
	mustNil(t, err)
	if len(rows) != 2 || rows[0].Kind != RowLatestSeries || rows[1].Kind != RowLatestMovies {
		t.Errorf("home: %v", rowKinds(rows))
	}
	page, err := a.Activity(ctx, ActivityQuery{PageSize: 1})
	mustNil(t, err)
	if len(page.Entries) != 1 || page.Entries[0].Text.String() != "activity.libraries_reordered (names=Shows, Movies)" {
		t.Errorf("activity log: %+v", page.Entries)
	}
}

func TestSaveProgress(t *testing.T) {
	a, p, films, _ := catalogApp(t)
	ctx := context.Background()
	page, err := a.ListMovies(ctx, p, ListQuery{LibraryID: &films.ID, PageSize: 1})
	mustNil(t, err)
	big := page.Items[0].Item // one minute long (NFO)
	if big.Runtime != time.Minute {
		t.Fatalf("runtime: %v", big.Runtime)
	}
	mustNil(t, a.SaveProgress(ctx, p, big.ID, 58*time.Second))
	v, _, err := a.Movie(ctx, p, big.ID)
	mustNil(t, err)
	if !v.UserData.Played || v.UserData.PlayCount != 1 || v.UserData.Position != 0 || v.UserData.LastPlayedAt == nil {
		t.Errorf("end of playback: %+v", v.UserData)
	}
	if err := a.SaveProgress(ctx, p, big.ID, -time.Second); !isKind(err, domain.ErrInvalid) {
		t.Errorf("negative position: %v", err)
	}
	if err := a.SaveProgress(ctx, p, films.ID, time.Minute); !isKind(err, domain.ErrNotFound) {
		t.Errorf("not a movie: %v", err)
	}
}

// next waits for an event of type T (others are skipped).
func next[T domain.Event](t *testing.T, s *Subscription) T {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		e, err := s.Next(ctx)
		if err != nil {
			var zero T
			t.Fatalf("waiting for a %T: %v", zero, err)
		}
		if e, ok := e.(T); ok {
			return e
		}
	}
}

func TestEvents(t *testing.T) {
	a, _ := startMediaApp(t)
	_, admin := setupAdmin(t, a)
	ctx := context.Background()
	adminSub := a.Subscribe(admin)
	defer adminSub.Close()
	other := domain.Principal{Account: domain.Account{ID: domain.NewID()}, Profile: &domain.Profile{ID: domain.NewID()}}
	otherSub := a.Subscribe(other)
	defer otherSub.Close()

	lib, err := a.CreateLibrary(ctx, "Movies", domain.LibraryMovies, []string{testRoot("Movies")}, "")
	mustNil(t, err)
	next[domain.LibrariesChanged](t, adminSub)
	if scanned := next[domain.LibraryScanned](t, adminSub); scanned.LibraryID != lib.ID || scanned.Added != 6 {
		t.Errorf("scan: %+v", scanned)
	}
	if changed := next[domain.ItemsChanged](t, adminSub); changed.LibraryID != lib.ID || len(changed.ItemIDs) == 0 {
		t.Errorf("items: %+v", changed)
	}
	waitIdle(t, a)

	page, err := a.ListMovies(ctx, admin, ListQuery{PageSize: 1})
	mustNil(t, err)
	mustNil(t, a.SetFavorite(ctx, admin, page.Items[0].Item.ID, true))
	if e := next[domain.UserDataChanged](t, adminSub); e.ProfileID != admin.Profile.ID || len(e.ItemIDs) != 1 {
		t.Errorf("profile data: %+v", e)
	}

	// The other account sees libraries and items, but never the scan (administrators only) or
	// another profile's data.
	ctxShort, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	for {
		e, err := otherSub.Next(ctxShort)
		if errors.Is(err, context.DeadlineExceeded) {
			break
		}
		mustNil(t, err)
		switch e.(type) {
		case domain.LibraryScanned, domain.UserDataChanged, domain.DownloadsChanged, domain.RequestsChanged:
			t.Errorf("event received by mistake: %T", e)
		case domain.Resync, domain.LibrariesChanged, domain.ItemsChanged, domain.ThemesChanged:
		}
	}
}

func TestEventsResyncWhenLagging(t *testing.T) {
	a, _ := newTestApp(t)
	s := a.Subscribe(domain.Principal{})
	defer s.Close()
	for range eventBuffer + 10 {
		a.bus.Publish(domain.LibrariesChanged{})
	}
	ctx := context.Background()
	if e, err := s.Next(ctx); err != nil || e != (domain.Resync{}) {
		t.Errorf("first event after a loss: %v %v", e, err)
	}
	if e, err := s.Next(ctx); err != nil || e != (domain.LibrariesChanged{}) {
		t.Errorf("then the rest: %v %v", e, err)
	}
}
