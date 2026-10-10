package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/laterna-project/laterna/internal/arr"
	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/store"
)

// What is coming (docs/design/home.md): the calendars of Sonarr, Radarr and Lidarr, read in the
// background and kept in memory, so that the home page never waits for an instance.
const (
	// jobRefreshUpcoming reads the calendars again.
	jobRefreshUpcoming = "upcoming.refresh"
	// upcomingEvery is how often the calendars are read; upcomingAfterImport is how long after an
	// import an instance announces.
	upcomingEvery       = 30 * time.Minute
	upcomingAfterImport = time.Minute
	// upcomingAhead is how far the row looks. upcomingLate keeps what has aired or come out and is
	// not there yet: it is still on its way.
	upcomingAhead = 14 * 24 * time.Hour
	upcomingLate  = 24 * time.Hour
	// maxUpcoming caps what is kept of one instance.
	maxUpcoming = 500
)

// upcomingCache is what the instances expect, as of their last reading.
type upcomingCache struct {
	mu sync.RWMutex
	// byKind keeps each instance's releases apart: one that does not answer keeps its last ones.
	byKind map[arr.Kind][]upcomingEntry
	// sorted is all of them, soonest first.
	sorted []upcomingEntry
}

// upcomingEntry is a release and where it can land, which decides who sees it.
type upcomingEntry struct {
	domain.Upcoming
	// places are the libraries the release belongs to. everyPlace is true when nothing says which of
	// them it will land in: a profile then has to be allowed in all of them.
	places     []upcomingPlace
	everyPlace bool
}

// upcomingPlace is a library a release can land in, and what the catalog has of it there.
type upcomingPlace struct {
	library domain.ID
	// item is the series or the artist the catalog has in that library, with its images.
	item   *domain.ID
	images []domain.Image
	// age is the age its rating maps to; rated is false without a known rating.
	age   int
	rated bool
}

var upcomingKinds = map[arr.Kind]domain.UpcomingKind{
	arr.Sonarr: domain.UpcomingEpisode, arr.Radarr: domain.UpcomingMovie, arr.Lidarr: domain.UpcomingAlbum,
}

// upcomingProviders names the ID a release carries, as the catalog knows it.
var upcomingProviders = map[arr.Kind]string{
	arr.Sonarr: "tvdb", arr.Radarr: "tmdb", arr.Lidarr: "musicbrainz_artist",
}

var upcomingFamilies = map[arr.Kind]domain.RequestKind{
	arr.Sonarr: domain.RequestSeries, arr.Radarr: domain.RequestMovie, arr.Lidarr: domain.RequestMusic,
}

// followUpcoming asks for a reading of the calendars after a delay, if an instance is linked.
func (a *App) followUpcoming(ctx context.Context, after time.Duration) {
	linked := false
	for _, k := range arr.Kinds {
		if _, ok, err := a.loadIntegration(ctx, string(k)); err == nil && ok {
			linked = true
		}
	}
	a.upcoming.mu.RLock()
	known := len(a.upcoming.sorted) > 0
	a.upcoming.mu.RUnlock()
	// Without any instance left there may still be releases to forget.
	if !linked && !known {
		return
	}
	if err := a.store.Write(ctx, func(q store.Q) error {
		return a.jobs.EnqueueAfter(ctx, q, jobRefreshUpcoming, "", priorityBackground, after)
	}); err != nil {
		a.log.WarnContext(ctx, "upcoming: cannot schedule a refresh", "err", err)
		return
	}
	a.jobs.Kick()
}

// refreshUpcoming reads the calendar of each linked instance (job upcoming.refresh) and works out
// where each release can land. An instance that does not answer keeps the releases it had.
func (a *App) refreshUpcoming(ctx context.Context, _ string) error {
	now := a.now()
	read := a.store.Read()
	libs, err := read.Libraries(ctx)
	if err != nil {
		return err
	}
	dests, err := read.RequestDestinations(ctx)
	if err != nil {
		return err
	}
	fresh := map[arr.Kind][]upcomingEntry{}
	var errs []error
	for _, k := range arr.Kinds {
		s, ok, err := a.loadIntegration(ctx, string(k))
		if err != nil {
			return err
		}
		if !ok {
			fresh[k] = nil
			continue
		}
		releases, err := a.arrClient(k, s).Calendar(ctx, now.Add(-upcomingLate), now.Add(upcomingAhead))
		if err != nil {
			errs = append(errs, fmt.Errorf("%s calendar: %w", k, err))
			continue
		}
		entries, err := a.placeUpcoming(ctx, k, releases, libs, dests)
		if err != nil {
			return err
		}
		fresh[k] = entries
	}

	a.upcoming.mu.Lock()
	if a.upcoming.byKind == nil {
		a.upcoming.byKind = map[arr.Kind][]upcomingEntry{}
	}
	var all []upcomingEntry
	for _, k := range arr.Kinds {
		if entries, ok := fresh[k]; ok {
			a.upcoming.byKind[k] = entries
		}
		all = append(all, a.upcoming.byKind[k]...)
	}
	slices.SortStableFunc(all, func(x, y upcomingEntry) int {
		return cmp.Or(x.At.Compare(y.At), cmp.Compare(x.Parent, y.Parent), cmp.Compare(x.Season, y.Season), cmp.Compare(x.Episode, y.Episode))
	})
	a.upcoming.sorted = all
	a.upcoming.mu.Unlock()
	return errors.Join(errs...)
}

// placeUpcoming turns the releases of an instance into entries: what the instance already has is
// left out, and each release gets the libraries it can land in.
func (a *App) placeUpcoming(ctx context.Context, k arr.Kind, releases []arr.Release, libs []domain.Library, dests []domain.RequestDestination) ([]upcomingEntry, error) {
	releases = slices.DeleteFunc(releases, func(r arr.Release) bool { return r.HasFile })
	if len(releases) > maxUpcoming {
		slices.SortFunc(releases, func(x, y arr.Release) int { return x.At.Compare(y.At) })
		releases = releases[:maxUpcoming]
	}
	if len(releases) == 0 {
		return nil, nil
	}
	read := a.store.Read()
	ids := make([]string, 0, len(releases))
	for _, r := range releases {
		ids = append(ids, r.ExternalID)
	}
	slices.Sort(ids)
	inCatalog, err := read.ItemsWithExternalIDs(ctx, upcomingProviders[k], slices.Compact(ids))
	if err != nil {
		return nil, err
	}
	var itemIDs []domain.ID
	for _, items := range inCatalog {
		for _, it := range items {
			itemIDs = append(itemIDs, it.ItemID)
		}
	}
	// Read as nobody in particular: who sees what is decided when a profile asks.
	views, err := read.ViewsByID(ctx, domain.Viewer{}, itemIDs)
	if err != nil {
		return nil, err
	}
	var ofKind []domain.ID
	for _, l := range libs {
		if l.Kind == libraryKind(k) {
			ofKind = append(ofKind, l.ID)
		}
	}

	out := make([]upcomingEntry, 0, len(releases))
	for _, r := range releases {
		e := upcomingEntry{Upcoming: domain.Upcoming{
			Kind: upcomingKinds[k], Title: r.Title, Parent: r.Parent, Season: r.Season, Episode: r.Episode,
			At: r.At, AllDay: r.AllDay, Poster: r.Poster,
		}}
		age, rated := domain.RatingAge(r.Certification)
		items := inCatalog[r.ExternalID]
		switch {
		case len(items) > 0 && k == arr.Radarr:
			// The catalog has the movie: nothing is coming.
			continue
		case len(items) > 0:
			for _, it := range items {
				p := upcomingPlace{library: it.LibraryID, item: &it.ItemID, age: age, rated: rated}
				if v, ok := views[it.ItemID]; ok {
					p.images = v.Images
					// The rating the catalog shows is the one that hides the series there.
					p.age, p.rated = domain.RatingAge(v.Item.OfficialRating)
				}
				e.places = append(e.places, p)
			}
		default:
			if lib, ok := destinationLibrary(dests, upcomingFamilies[k], r.RootFolder); ok {
				e.places = []upcomingPlace{{library: lib, age: age, rated: rated}}
				break
			}
			// Nothing says where it will land: any library of its kind.
			e.everyPlace = true
			for _, lib := range ofKind {
				e.places = append(e.places, upcomingPlace{library: lib, age: age, rated: rated})
			}
		}
		if len(e.places) > 0 {
			out = append(out, e)
		}
	}
	return out, nil
}

// destinationLibrary finds the library the requests of a family send to a root folder of the
// instance.
func destinationLibrary(dests []domain.RequestDestination, family domain.RequestKind, root string) (domain.ID, bool) {
	clean := func(p string) string { return strings.TrimRight(strings.ReplaceAll(p, `\`, "/"), "/") }
	if root = clean(root); root == "" {
		return domain.ID{}, false
	}
	for _, d := range dests {
		if d.Kind == family && clean(d.RootFolder) == root {
			return d.LibraryID, true
		}
	}
	return domain.ID{}, false
}

// upcomingFor lists what is coming for a profile, soonest first: releases that land in a library it
// may browse, under its parental control (the rating of the series, or the one the instance gives;
// music has none).
func (a *App) upcomingFor(v domain.Viewer, limit int) []domain.Upcoming {
	oldest := a.now().Add(-upcomingLate)
	a.upcoming.mu.RLock()
	defer a.upcoming.mu.RUnlock()
	var out []domain.Upcoming
	for _, e := range a.upcoming.sorted {
		if len(out) == limit {
			break
		}
		if e.At.Before(oldest) {
			continue
		}
		place, ok := e.placeFor(v)
		if !ok {
			continue
		}
		u := e.Upcoming
		u.ItemID, u.Images = place.item, place.images
		if len(u.Images) > 0 {
			u.Poster = ""
		} else {
			a.rememberPoster(u.Poster)
		}
		out = append(out, u)
	}
	return out
}

// placeFor picks the place a profile sees the release through.
func (e upcomingEntry) placeFor(v domain.Viewer) (upcomingPlace, bool) {
	allowed := func(p upcomingPlace) bool {
		return v.AllowsLibrary(p.library) && (e.Kind == domain.UpcomingAlbum || v.Parental.Allows(p.age, p.rated))
	}
	if e.everyPlace {
		if len(e.places) == 0 || slices.ContainsFunc(e.places, func(p upcomingPlace) bool { return !allowed(p) }) {
			return upcomingPlace{}, false
		}
		return e.places[0], true
	}
	for _, p := range e.places {
		if allowed(p) {
			return p, true
		}
	}
	return upcomingPlace{}, false
}
