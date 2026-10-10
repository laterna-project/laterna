package app

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/laterna-project/laterna/internal/arr/arrtest"
	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/store"
)

// upcomingOf is the row of what is coming on the home page of a profile, as "Parent Title" lines.
func upcomingOf(t *testing.T, a *App, p domain.Principal) ([]string, []domain.Upcoming) {
	t.Helper()
	rows, err := a.Home(context.Background(), p, 0, true)
	mustNil(t, err)
	var titles []string
	list := []domain.Upcoming{}
	for _, r := range rows {
		if r.Kind != RowUpcoming {
			continue
		}
		if len(r.Items) != 0 || r.Title.Key != "home.upcoming" {
			t.Errorf("row: %+v", r)
		}
		list = r.Upcoming
		for _, u := range r.Upcoming {
			titles = append(titles, u.Title)
		}
	}
	return titles, list
}

func TestUpcoming(t *testing.T) {
	a, clk, admin, sonarr, radarr, shows, movies := requestApp(t)
	ctx := context.Background()
	now := clk.now()
	at := func(d time.Duration) string { return now.Add(d).UTC().Format(time.RFC3339) }
	day := 24 * time.Hour

	// A second series library, for the children.
	kids := domain.Library{ID: domain.NewID(), Name: "Kids", Kind: domain.LibraryShows, Paths: []string{t.TempDir()}, CreatedAt: now, UpdatedAt: now}
	mustNil(t, a.store.Write(ctx, func(q store.Q) error { return q.CreateLibrary(ctx, kids) }))
	frieren := putInCatalog(t, a, shows, "Frieren", "tvdb", 101, 2)
	mustNil(t, a.store.Write(ctx, func(q store.Q) error {
		_, err := q.SetMetadata(ctx, frieren, domain.Metadata{Title: "Frieren", SortTitle: "frieren", OfficialRating: "TV-14", ProviderIDs: map[string]string{"tvdb": "101"}}, now)
		return err
	}))
	putInCatalog(t, a, movies, "Already Here", "tmdb", 55, 0)

	series := func(title string, tvdb int, more map[string]any) map[string]any {
		s := map[string]any{"title": title, "tvdbId": tvdb, "rootFolderPath": "/data/media/shows", "images": []any{map[string]any{"coverType": "poster", "remoteUrl": "https://artworks.example/" + title + ".jpg"}}}
		for k, v := range more {
			s[k] = v
		}
		return s
	}
	sonarr.Set(func(s *arrtest.Server) {
		s.Calendar = []map[string]any{
			{"title": "In Two Days", "seasonNumber": 2, "episodeNumber": 5, "airDateUtc": at(2 * day), "series": series("Frieren", 101, nil)},
			// Aired three hours ago and not there yet: still on its way.
			{"title": "Just Aired", "seasonNumber": 2, "episodeNumber": 4, "airDateUtc": at(-3 * time.Hour), "series": series("Frieren", 101, nil)},
			// The instance has it: nothing is coming.
			{"title": "Downloaded", "seasonNumber": 2, "episodeNumber": 3, "airDateUtc": at(-5 * time.Hour), "hasFile": true, "series": series("Frieren", 101, nil)},
			// A series the catalog does not have yet.
			{"title": "Pilot", "seasonNumber": 1, "episodeNumber": 1, "airDateUtc": at(3 * day), "series": series("Monster", 103, map[string]any{"certification": "TV-MA"})},
		}
	})
	radarr.Set(func(s *arrtest.Server) {
		s.Calendar = []map[string]any{
			{"title": "Suzume", "tmdbId": 916224, "digitalRelease": at(5 * day), "rootFolderPath": "/data/media/movies", "certification": "PG"},
			{"title": "Already Here", "tmdbId": 55, "digitalRelease": at(4 * day), "rootFolderPath": "/data/media/movies"},
		}
	})

	// Nothing before the calendars are read, and nothing for a client that does not ask.
	if got, _ := upcomingOf(t, a, admin); got != nil {
		t.Errorf("before the first reading: %v", got)
	}
	mustNil(t, a.refreshUpcoming(ctx, ""))
	rows, err := a.Home(ctx, admin, 0, false)
	mustNil(t, err)
	if slices.ContainsFunc(rows, func(r HomeRow) bool { return r.Kind == RowUpcoming }) {
		t.Error("the row was sent to a client that did not ask for it")
	}

	// Soonest first; the series in the catalog is linked, the others carry the instance's poster.
	got, list := upcomingOf(t, a, admin)
	if !slices.Equal(got, []string{"Just Aired", "In Two Days", "Pilot", "Suzume"}) {
		t.Fatalf("administrator: %v", got)
	}
	if u := list[1]; u.Kind != domain.UpcomingEpisode || u.Parent != "Frieren" || u.Season != 2 || u.Episode != 5 || u.AllDay ||
		!u.At.Equal(now.Add(2*day).Truncate(time.Second)) || u.ItemID == nil || *u.ItemID != frieren {
		t.Errorf("episode: %+v", u)
	}
	if u := list[2]; u.ItemID != nil || u.Poster != "https://artworks.example/Monster.jpg" {
		t.Errorf("series not in the catalog: %+v", u)
	}
	if u := list[3]; u.Kind != domain.UpcomingMovie || !u.AllDay || u.Parent != "" || u.ItemID != nil {
		t.Errorf("movie: %+v", u)
	}
	// Posters are served by the server: their address is remembered each time the row is built.
	if src, ok, err := a.posterSource(ctx, posterKey("https://artworks.example/Monster.jpg")); err != nil || !ok || src == "" {
		t.Errorf("poster of an upcoming series: %q %v %v", src, ok, err)
	}
	// The row follows the size asked for.
	rows, err = a.Home(ctx, admin, 2, true)
	mustNil(t, err)
	for _, r := range rows {
		if r.Kind == RowUpcoming && len(r.Upcoming) != 2 {
			t.Errorf("row of 2: %d", len(r.Upcoming))
		}
	}

	// An account limited to movies only sees the movie.
	_, err = a.CreateAccount(ctx, admin, NewAccount{Username: "Tom", Password: "a-password", Libraries: &domain.LibraryAccess{IDs: []domain.ID{movies.ID}}})
	mustNil(t, err)
	_, tom := login(t, a, "Tom", "a-password")
	if got, _ := upcomingOf(t, a, tom); !slices.Equal(got, []string{"Suzume"}) {
		t.Errorf("movies only: %v", got)
	}
	// Nothing says which series library Monster will land in: an account that may not browse them
	// all does not see it. Frieren is in the one it may browse.
	_, err = a.CreateAccount(ctx, admin, NewAccount{Username: "Léa", Password: "a-password", Libraries: &domain.LibraryAccess{IDs: []domain.ID{shows.ID}}})
	mustNil(t, err)
	_, lea := login(t, a, "Léa", "a-password")
	if got, _ := upcomingOf(t, a, lea); !slices.Equal(got, []string{"Just Aired", "In Two Days"}) {
		t.Errorf("one series library, no destination: %v", got)
	}
	// A destination says where that root folder lands.
	name, root, profile := "Anime", "/data/media/shows", 1
	_, err = a.CreateRequestDestination(ctx, admin, DestinationChanges{Name: &name, Kind: domain.RequestSeries, LibraryID: &shows.ID, RootFolder: &root, QualityProfileID: &profile})
	mustNil(t, err)
	mustNil(t, a.refreshUpcoming(ctx, ""))
	if got, _ := upcomingOf(t, a, lea); !slices.Equal(got, []string{"Just Aired", "In Two Days", "Pilot"}) {
		t.Errorf("one series library, with a destination: %v", got)
	}

	// Parental control: the rating of the series in the catalog (TV-14), else the instance's
	// (TV-MA, PG).
	ten := 10
	_, err = a.CreateAccount(ctx, admin, NewAccount{Username: "Zoé", Password: "a-password", Parental: &domain.ParentalControl{MaxAge: &ten, BlockUnrated: true}})
	mustNil(t, err)
	_, zoe := login(t, a, "Zoé", "a-password")
	if got, _ := upcomingOf(t, a, zoe); !slices.Equal(got, []string{"Suzume"}) {
		t.Errorf("age 10: %v", got)
	}

	// Time passes without a reading: what aired more than a day ago is no longer listed.
	clk.advance(22 * time.Hour)
	if got, _ := upcomingOf(t, a, admin); !slices.Equal(got, []string{"In Two Days", "Pilot", "Suzume"}) {
		t.Errorf("22 hours later: %v", got)
	}

	// An instance that does not answer keeps what it had; the other one is read again.
	sonarr.Close()
	radarr.Set(func(s *arrtest.Server) { s.Calendar = nil })
	if err := a.refreshUpcoming(ctx, ""); err == nil {
		t.Error("Sonarr is down: want an error, for the job to be tried again")
	}
	if got, _ := upcomingOf(t, a, admin); !slices.Equal(got, []string{"In Two Days", "Pilot"}) {
		t.Errorf("Sonarr down, Radarr empty: %v", got)
	}
	// An integration removed takes its releases with it.
	mustNil(t, a.DeleteIntegration(ctx, admin, domain.IntegrationSonarr))
	mustNil(t, a.refreshUpcoming(ctx, ""))
	if got, _ := upcomingOf(t, a, admin); got != nil {
		t.Errorf("after Sonarr was unlinked: %v", got)
	}
}

func TestUpcomingPlaces(t *testing.T) {
	a, b := domain.NewID(), domain.NewID()
	twelve := 12
	free := domain.Viewer{}
	onlyA := domain.Viewer{Libraries: []domain.ID{a}}
	child := domain.Viewer{Parental: domain.ParentalControl{MaxAge: &twelve}}
	entry := func(kind domain.UpcomingKind, every bool, places ...upcomingPlace) upcomingEntry {
		return upcomingEntry{Upcoming: domain.Upcoming{Kind: kind}, places: places, everyPlace: every}
	}
	for name, c := range map[string]struct {
		e    upcomingEntry
		v    domain.Viewer
		want *domain.ID
	}{
		"first allowed place":         {entry(domain.UpcomingEpisode, false, upcomingPlace{library: b}, upcomingPlace{library: a}), onlyA, &a},
		"no allowed place":            {entry(domain.UpcomingEpisode, false, upcomingPlace{library: b}), onlyA, nil},
		"any of them, one forbidden":  {entry(domain.UpcomingMovie, true, upcomingPlace{library: a}, upcomingPlace{library: b}), onlyA, nil},
		"any of them, all allowed":    {entry(domain.UpcomingMovie, true, upcomingPlace{library: a}, upcomingPlace{library: b}), free, &a},
		"any of them, none":           {entry(domain.UpcomingMovie, true), free, nil},
		"too old for the child":       {entry(domain.UpcomingEpisode, false, upcomingPlace{library: a, age: 14, rated: true}), child, nil},
		"unrated passes without rule": {entry(domain.UpcomingEpisode, false, upcomingPlace{library: a}), child, &a},
		"music has no rating":         {entry(domain.UpcomingAlbum, false, upcomingPlace{library: a, age: 18, rated: true}), child, &a},
	} {
		p, ok := c.e.placeFor(c.v)
		if ok != (c.want != nil) || ok && p.library != *c.want {
			t.Errorf("%s: %+v %v", name, p, ok)
		}
	}
	if _, ok := destinationLibrary([]domain.RequestDestination{{Kind: domain.RequestSeries, LibraryID: a, RootFolder: `D:\media\shows\`}}, domain.RequestSeries, "D:/media/shows"); !ok {
		t.Error("a root folder is the same with either slash and a trailing one")
	}
	if _, ok := destinationLibrary([]domain.RequestDestination{{Kind: domain.RequestMovie, LibraryID: a, RootFolder: "/data"}}, domain.RequestSeries, "/data"); ok {
		t.Error("a destination of another family")
	}
}
