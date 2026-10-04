package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
)

// catalogFixture fills a database: movies with various titles, dates, ratings and genres, a series
// with two seasons, and a profile.
type catalogFixture struct {
	st      *Store
	lib     domain.Library
	profile domain.ID
	movies  map[string]domain.Item // by title
	series  domain.Item
	seasons [2]domain.Item
	eps     []domain.Item // S1E1, S1E2, S2E1
	files   map[domain.ID]domain.ID
}

func newCatalogFixture(t *testing.T) *catalogFixture {
	t.Helper()
	st, _ := openTemp(t)
	ctx := context.Background()
	f := &catalogFixture{st: st, movies: map[string]domain.Item{}, files: map[domain.ID]domain.ID{}}
	f.lib = newLibrary("Tout", domain.LibraryMovies, "/m")
	acc := newAccount("famille", true)
	profile := domain.Profile{ID: domain.NewID(), AccountID: acc.ID, Name: "Chloé", CreatedAt: t0, UpdatedAt: t0}
	f.profile = profile.ID

	type movie struct {
		title, sort, date string
		year              int
		rating            float64
		genres            []string
	}
	movies := []movie{
		{"Amélie", "amelie", "2001-04-25", 2001, 7.9, []string{"Comédie", "Romance"}},
		{"Le Voyage de Chihiro", "voyage de chihiro", "2001-07-20", 2001, 8.5, []string{"Animation"}},
		{"Alien", "alien", "", 1979, 8.5, []string{"Science-fiction"}},
		{"Brazil", "brazil", "1985-02-20", 1985, 7.9, []string{"Science-fiction", "Comédie"}},
		{"Casablanca", "casablanca", "1942-11-26", 1942, 8.2, nil},
		{"Zodiac", "zodiac", "2007-03-02", 2007, 0, []string{"Thriller"}},
		{"Alien", "alien", "", 1979, 6.0, nil}, // same title twice: the order must stay stable
	}
	var steps []error
	add := func(err error) { steps = append(steps, err) }
	mustWrite(t, st, func(q Q) error {
		add(q.CreateAccount(ctx, acc, "h"))
		add(q.CreateProfile(ctx, profile, ""))
		add(q.CreateLibrary(ctx, f.lib))
		for i, m := range movies {
			it := domain.Item{
				ID: domain.NewID(), LibraryID: f.lib.ID, Kind: domain.ItemMovie, GroupKey: fmt.Sprintf("movie:%d", i),
				Title: m.title, SortTitle: m.sort, AddedAt: t0.Add(time.Duration(i) * time.Hour), UpdatedAt: t0,
			}
			add(q.CreateItem(ctx, it))
			_, err := q.SetMetadata(ctx, it.ID, domain.Metadata{
				Title: m.title, SortTitle: m.sort, Year: m.year, PremiereDate: m.date, CommunityRating: m.rating, Genres: m.genres,
			}, t0)
			add(err)
			it.Year, it.PremiereDate, it.CommunityRating = m.year, m.date, m.rating
			if _, dup := f.movies[m.title]; !dup {
				f.movies[m.title] = it
			}
			f.link(t, q, it, fmt.Sprintf("/m/%d.mkv", i))
		}

		f.series = domain.Item{ID: domain.NewID(), LibraryID: f.lib.ID, Kind: domain.ItemSeries, GroupKey: "series:x", Title: "Série Test", SortTitle: "serie test", AddedAt: t0, UpdatedAt: t0}
		add(q.CreateItem(ctx, f.series))
		for n := range 2 {
			s := domain.Item{ID: domain.NewID(), LibraryID: f.lib.ID, Kind: domain.ItemSeason, ParentID: &f.series.ID, GroupKey: fmt.Sprintf("season:%d", n+1), Title: fmt.Sprintf("Saison %d", n+1), SortTitle: fmt.Sprintf("%04d", n+1), AddedAt: t0, UpdatedAt: t0}
			add(q.CreateItem(ctx, s))
			add(q.CreateSeason(ctx, domain.Season{ItemID: s.ID, SeriesID: f.series.ID, Number: n + 1}))
			f.seasons[n] = s
		}
		for _, se := range [][2]int{{1, 2}, {1, 1}, {2, 1}} { // created out of order
			e := domain.Item{ID: domain.NewID(), LibraryID: f.lib.ID, Kind: domain.ItemEpisode, ParentID: &f.seasons[se[0]-1].ID, GroupKey: fmt.Sprintf("episode:%d:%d", se[0], se[1]), Title: fmt.Sprintf("Épisode %d", se[1]), SortTitle: fmt.Sprintf("%04d", se[1]), AddedAt: t0, UpdatedAt: t0}
			add(q.CreateItem(ctx, e))
			add(q.CreateEpisode(ctx, domain.Episode{ItemID: e.ID, SeriesID: f.series.ID, SeasonID: f.seasons[se[0]-1].ID, SeasonNumber: se[0], Number: se[1]}))
			f.link(t, q, e, fmt.Sprintf("/m/serie/%d-%d.mkv", se[0], se[1]))
			f.eps = append(f.eps, e)
		}
		f.eps[0], f.eps[1] = f.eps[1], f.eps[0] // S1E1, S1E2, S2E1
		return errors.Join(steps...)
	})
	return f
}

// viewer returns the fixture's profile, unrestricted.
func (f *catalogFixture) viewer() domain.Viewer { return domain.Viewer{ProfileID: f.profile} }

func (f *catalogFixture) link(t *testing.T, q Q, it domain.Item, path string) {
	t.Helper()
	file := domain.MediaFile{ID: domain.NewID(), LibraryID: it.LibraryID, Path: path, Size: 1, ModTime: t0, Fingerprint: path}
	if err := errors.Join(q.CreateFile(context.Background(), file, t0), q.LinkFile(context.Background(), it.ID, file.ID, "", 0)); err != nil {
		t.Fatal(err)
	}
	f.files[it.ID] = file.ID
}

// all walks every page of a query and returns the titles (and years) in order.
func (f *catalogFixture) all(t *testing.T, iq ItemQuery) []string {
	t.Helper()
	if iq.Viewer.ProfileID == (domain.ID{}) {
		iq.Viewer = f.viewer()
	}
	iq.Limit = 2
	var out []string
	seen := map[domain.ID]bool{}
	for range 20 {
		cards, next, err := f.st.Read().Items(context.Background(), iq)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range cards {
			if seen[c.Item.ID] {
				t.Fatalf("%s seen twice", c.Item.Title)
			}
			seen[c.Item.ID] = true
			out = append(out, fmt.Sprintf("%s %d", c.Item.Title, c.Item.Year))
		}
		if next == nil {
			n, err := f.st.Read().CountItems(context.Background(), iq)
			if err != nil || n != len(out) {
				t.Errorf("count %d (%v), %d items walked", n, err, len(out))
			}
			return out
		}
		iq.After = next
	}
	t.Fatal("endless pagination")
	return nil
}

func TestItemsPaginationAndSorts(t *testing.T) {
	f := newCatalogFixture(t)
	movies := func(sort domain.ItemSort, reverse bool) []string {
		return f.all(t, ItemQuery{Kind: domain.ItemMovie, Sort: sort, Reverse: reverse})
	}
	byTitle := movies(domain.SortTitle, false)
	want := []string{"Alien 1979", "Alien 1979", "Amélie 2001", "Brazil 1985", "Casablanca 1942", "Le Voyage de Chihiro 2001", "Zodiac 2007"}
	if !slices.Equal(byTitle, want) {
		t.Errorf("by title: %v", byTitle)
	}
	reversed := movies(domain.SortTitle, true)
	slices.Reverse(reversed)
	if !slices.Equal(reversed, want) {
		t.Errorf("by title, reversed: %v", reversed)
	}
	if got := movies(domain.SortAdded, false); got[0] != "Alien 1979" || got[6] != "Amélie 2001" {
		t.Errorf("by date added (newest first): %v", got)
	}
	// Unknown date: the year stands in for it ("1979" before "1985-02-20").
	if got := movies(domain.SortReleased, false); !slices.Equal(got[:3], []string{"Zodiac 2007", "Le Voyage de Chihiro 2001", "Amélie 2001"}) || got[4] != "Alien 1979" {
		t.Errorf("by release date: %v", got)
	}
	if got := movies(domain.SortRating, false); got[0] != "Alien 1979" || got[6] != "Zodiac 2007" {
		t.Errorf("by rating: %v", got)
	}
	if got := f.all(t, ItemQuery{Kind: domain.ItemSeries, Sort: domain.SortTitle}); !slices.Equal(got, []string{"Série Test 0"}) {
		t.Errorf("series: %v", got)
	}
}

func TestItemsFilters(t *testing.T) {
	f := newCatalogFixture(t)
	ctx := context.Background()
	q := ItemQuery{Kind: domain.ItemMovie, Sort: domain.SortTitle}

	q.Genre = "comédie" // case does not matter
	if got := f.all(t, q); !slices.Equal(got, []string{"Amélie 2001", "Brazil 1985"}) {
		t.Errorf("genre: %v", got)
	}
	q.Genre = ""

	amelie, brazil := f.movies["Amélie"], f.movies["Brazil"]
	mustWrite(t, f.st, func(w Q) error {
		return errors.Join(
			w.SetPlayed(ctx, f.profile, []domain.ID{amelie.ID}, true, t0),
			w.SetFavorite(ctx, f.profile, brazil.ID, true, t0),
		)
	})
	played, unplayed := true, false
	q.Played = &played
	if got := f.all(t, q); !slices.Equal(got, []string{"Amélie 2001"}) {
		t.Errorf("played: %v", got)
	}
	q.Played = &unplayed
	if got := f.all(t, q); len(got) != 6 {
		t.Errorf("unplayed: %v", got)
	}
	q.Played, q.Favorite = nil, true
	if got := f.all(t, q); !slices.Equal(got, []string{"Brazil 1985"}) {
		t.Errorf("favorites: %v", got)
	}

	// A vanished file (grace period): the movie is no longer listed but its details can still be
	// read.
	mustWrite(t, f.st, func(w Q) error { return w.MarkFileMissing(ctx, f.files[brazil.ID], t0) })
	if got := f.all(t, q); len(got) != 0 {
		t.Errorf("vanished favorite still listed: %v", got)
	}
	c, err := f.st.Read().View(ctx, f.viewer(), brazil.ID)
	if err != nil || !c.UserData.Favorite {
		t.Errorf("details of a vanished movie: %+v %v", c, err)
	}
	if c, _ := f.st.Read().View(ctx, f.viewer(), amelie.ID); !c.UserData.Played || c.UserData.LastPlayedAt == nil {
		t.Errorf("user data: %+v", c.UserData)
	}
	// Another profile does not see the first one's data.
	if c, _ := f.st.Read().View(ctx, domain.Viewer{ProfileID: domain.NewID()}, amelie.ID); c.UserData.Played {
		t.Error("data from another profile")
	}
}

func TestSeriesSeasonsEpisodes(t *testing.T) {
	f := newCatalogFixture(t)
	ctx := context.Background()
	read := f.st.Read()

	mustWrite(t, f.st, func(w Q) error { return w.SetPlayed(ctx, f.profile, []domain.ID{f.eps[0].ID}, true, t0) })
	c, err := read.View(ctx, f.viewer(), f.series.ID)
	if err != nil || c.EpisodeCount != 3 || c.UnplayedCount != 2 {
		t.Fatalf("series: %+v %v", c, err)
	}
	seasons, err := read.Seasons(ctx, f.viewer(), f.series.ID)
	if err != nil || len(seasons) != 2 || seasons[0].Season.Number != 1 || seasons[0].EpisodeCount != 2 || seasons[0].UnplayedCount != 1 || seasons[1].UnplayedCount != 1 {
		t.Fatalf("seasons: %+v %v", seasons, err)
	}
	eps, err := read.Episodes(ctx, f.viewer(), f.series.ID, nil)
	if err != nil || len(eps) != 3 {
		t.Fatalf("episodes: %v", err)
	}
	for i, want := range [][2]int{{1, 1}, {1, 2}, {2, 1}} {
		if e := eps[i].Episode; e == nil || e.SeasonNumber != want[0] || e.Number != want[1] || eps[i].SeriesTitle != "Série Test" {
			t.Errorf("episode %d: %+v", i, eps[i])
		}
	}
	if !eps[0].UserData.Played || eps[1].UserData.Played {
		t.Error("played state of the episodes")
	}
	if s2, _ := read.Episodes(ctx, f.viewer(), f.series.ID, &f.seasons[1].ID); len(s2) != 1 || s2[0].Episode.SeasonNumber != 2 {
		t.Errorf("episodes of season 2: %+v", s2)
	}

	// The whole series played, then season 2 gone: it is no longer listed.
	ids, err := read.EpisodeIDs(ctx, f.series)
	if err != nil || len(ids) != 3 {
		t.Fatalf("episodes of the series: %v %v", ids, err)
	}
	mustWrite(t, f.st, func(w Q) error {
		return errors.Join(w.SetPlayed(ctx, f.profile, ids, true, t0), w.MarkFileMissing(ctx, f.files[f.eps[2].ID], t0))
	})
	if c, _ := read.View(ctx, f.viewer(), f.series.ID); c.EpisodeCount != 2 || c.UnplayedCount != 0 || !c.UserData.Played {
		t.Errorf("series played, one episode gone: %+v", c)
	}
	if seasons, _ := read.Seasons(ctx, f.viewer(), f.series.ID); len(seasons) != 1 {
		t.Errorf("season with no episode present still listed: %d", len(seasons))
	}
}

func TestSearch(t *testing.T) {
	f := newCatalogFixture(t)
	ctx := context.Background()
	search := func(text string) []string {
		t.Helper()
		cards, err := f.st.Read().Search(ctx, f.viewer(), text, 10)
		if err != nil {
			t.Fatalf("%q: %v", text, err)
		}
		var out []string
		for _, c := range cards {
			out = append(out, c.Item.Title)
		}
		return out
	}
	tests := map[string][]string{
		"amelie":                  {"Amélie"}, // no accent
		"AMÉ":                     {"Amélie"}, // prefix, case
		"voyage chihiro":          {"Le Voyage de Chihiro"},
		"chihiro voyage":          {"Le Voyage de Chihiro"}, // any order
		"serie":                   {"Série Test"},
		"épisode":                 {"Épisode 1", "Épisode 1", "Épisode 2"}, // same relevance: by title
		`"; DROP TABLE items; --`: nil,                                     // words to search for, never interpreted
		"alien OR zodiac":         nil,                                     // FTS5 syntax is not interpreted
		"":                        nil,
		"   ":                     nil,
	}
	for text, want := range tests {
		if got := search(text); !slices.Equal(got, want) {
			t.Errorf("%q: %v, want %v", text, got, want)
		}
	}
	if got := search("s"); len(got) == 0 || got[0] != "Série Test" {
		t.Errorf("series must come before episodes: %v", got)
	}

	// The index follows title changes and deletions.
	zodiac := f.movies["Zodiac"]
	mustWrite(t, f.st, func(w Q) error {
		_, err := w.SetMetadata(ctx, zodiac.ID, domain.Metadata{Title: "Zodiaque", SortTitle: "zodiaque"}, t0)
		return err
	})
	if got := search("zodiaque"); !slices.Equal(got, []string{"Zodiaque"}) {
		t.Errorf("after a title change: %v", got)
	}
	mustWrite(t, f.st, func(w Q) error {
		return errors.Join(w.DeleteFile(ctx, f.files[zodiac.ID]), func() error { _, err := w.DeleteOrphanItems(ctx, f.lib.ID); return err }())
	})
	if got := search("zodiaque"); len(got) != 0 {
		t.Errorf("after deletion: %v", got)
	}
}

func TestGenres(t *testing.T) {
	f := newCatalogFixture(t)
	genres, err := f.st.Read().Genres(context.Background(), f.viewer(), &f.lib.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := []domain.GenreCount{{Name: "Animation", Count: 1}, {Name: "Comédie", Count: 2}, {Name: "Romance", Count: 1}, {Name: "Science-fiction", Count: 2}, {Name: "Thriller", Count: 1}}
	if !slices.Equal(genres, want) {
		t.Errorf("genres: %v", genres)
	}
	if all, _ := f.st.Read().Genres(context.Background(), f.viewer(), nil); len(all) != len(want) {
		t.Errorf("all libraries: %v", all)
	}
}

func TestQueryArgsMustMatch(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("a \"?\" without a value must panic")
		}
	}()
	var b query
	b.where("a = ? AND b = ?", 1)
}

// queryPlan returns the query plan (EXPLAIN QUERY PLAN), line by line.
func queryPlan(t *testing.T, st *Store, b *query) []string {
	t.Helper()
	rows, err := st.reader.QueryContext(context.Background(), "EXPLAIN QUERY PLAN "+b.String(), b.args...)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return plan
}

// A page sorted by title or by date added, for one library or all, goes through an index that gives
// its whole order: no full table scan, no sort in memory.
func TestItemsQueryPlan(t *testing.T) {
	f := newCatalogFixture(t)
	for _, tc := range []struct {
		lib   *domain.ID
		sort  domain.ItemSort
		index string
	}{
		{&f.lib.ID, domain.SortTitle, "items_library_title (library_id=? AND kind=? AND present=?)"},
		{&f.lib.ID, domain.SortAdded, "items_library_added (library_id=? AND kind=? AND present=?)"},
		{nil, domain.SortTitle, "items_kind_title (kind=? AND present=?)"},
		{nil, domain.SortAdded, "items_kind_added (kind=? AND present=?)"},
	} {
		b, err := itemsQuery(ItemQuery{Kind: domain.ItemMovie, LibraryID: tc.lib, Viewer: f.viewer(), Sort: tc.sort, Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		plan := queryPlan(t, f.st, b)
		if !slices.ContainsFunc(plan, func(l string) bool { return strings.HasPrefix(l, "SEARCH i USING INDEX "+tc.index) }) {
			t.Errorf("%s: index %s not used:\n%s", tc.sort, tc.index, strings.Join(plan, "\n"))
		}
		for _, line := range plan {
			if strings.HasPrefix(line, "SCAN i") || strings.Contains(line, "USE TEMP B-TREE FOR ORDER BY") {
				t.Errorf("%s: full scan or sort in memory: %s", tc.sort, line)
			}
		}
	}
}

// What a profile is allowed to see: the libraries of its account and the rating age (an episode or
// a season takes the one of its series).
func TestViewerFilter(t *testing.T) {
	f := newCatalogFixture(t)
	ctx := context.Background()
	ten, fourteen, sixteen, twelve := 10, 14, 16, 12
	rate := func(it domain.Item, age *int, genres ...string) {
		mustWrite(t, f.st, func(q Q) error {
			_, err := q.SetMetadata(ctx, it.ID, domain.Metadata{Title: it.Title, SortTitle: it.SortTitle, Year: it.Year, AgeRating: age, Genres: genres}, t0)
			return err
		})
	}
	rate(f.movies["Amélie"], &ten, "Comédie", "Romance")
	rate(f.movies["Alien"], &sixteen)
	rate(f.series, &fourteen)
	viewer := func(libs []domain.ID, maxAge *int, block bool) domain.Viewer {
		return domain.Viewer{ProfileID: f.profile, Libraries: libs, Parental: domain.ParentalControl{MaxAge: maxAge, BlockUnrated: block}}
	}
	movies := func(v domain.Viewer) []string {
		return f.all(t, ItemQuery{Kind: domain.ItemMovie, Viewer: v, Sort: domain.SortTitle})
	}
	series := func(v domain.Viewer) int {
		return len(f.all(t, ItemQuery{Kind: domain.ItemSeries, Viewer: v, Sort: domain.SortTitle}))
	}
	search := func(v domain.Viewer, text string) int {
		cards, err := f.st.Read().Search(ctx, v, text, 20)
		if err != nil {
			t.Fatal(err)
		}
		return len(cards)
	}

	// Up to age 12, unrated allowed: the Alien rated 16 goes away, not its namesake; the series
	// (14) and its episodes too.
	v := viewer(nil, &twelve, false)
	if got := movies(v); len(got) != 6 {
		t.Errorf("age 12: %v", got)
	}
	if n := series(v); n != 0 {
		t.Errorf("age 12: %d series", n)
	}
	if n := search(v, "épisode"); n != 0 {
		t.Errorf("age 12: %d episodes found", n)
	}
	for _, id := range []domain.ID{f.series.ID, f.seasons[0].ID, f.eps[0].ID, f.movies["Alien"].ID} {
		if _, err := f.st.Read().View(ctx, v, id); !IsNotFound(err) {
			t.Errorf("age 12: %s visible (%v)", id, err)
		}
	}
	// Unrated hidden: only Amélie (10) gets through.
	if got := movies(viewer(nil, &twelve, true)); !slices.Equal(got, []string{"Amélie 2001"}) {
		t.Errorf("age 12, unrated hidden: %v", got)
	}
	if g, _ := f.st.Read().Genres(ctx, viewer(nil, &twelve, true), nil); len(g) != 2 {
		t.Errorf("visible genres: %v", g)
	}
	// Only "unrated hidden": the two rated movies, the series and its episodes.
	if got := movies(viewer(nil, nil, true)); len(got) != 2 || series(viewer(nil, nil, true)) != 1 || search(viewer(nil, nil, true), "épisode") != 3 {
		t.Errorf("rated only: %v", got)
	}
	// Libraries: none, or the only one there is.
	if got := movies(viewer([]domain.ID{}, nil, false)); len(got) != 0 {
		t.Errorf("no library: %v", got)
	}
	if got := movies(viewer([]domain.ID{f.lib.ID}, nil, false)); len(got) != 7 {
		t.Errorf("own library: %v", got)
	}
	if n := search(viewer([]domain.ID{domain.NewID()}, nil, false), "a"); n != 0 {
		t.Errorf("other library: %d results", n)
	}
}
