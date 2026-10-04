package app

import (
	"context"
	"slices"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/store"
)

// Size of the home rows.
const (
	defaultRowSize = 20
	maxRowSize     = 50
)

// HomeRowKind is the kind of a home row. A client may display it its own way (and translate its
// title).
type HomeRowKind string

// Home rows, in display order.
const (
	RowResume HomeRowKind = "resume"
	RowNextUp HomeRowKind = "next_up"
	// Recommendations.
	RowRecommended       HomeRowKind = "recommended"
	RowBecauseYouWatched HomeRowKind = "because_you_watched"
	RowRecentAlbums      HomeRowKind = "recent_albums"
	RowLatestMovies      HomeRowKind = "latest_movies"
	RowLatestSeries      HomeRowKind = "latest_series"
	RowLatestAlbums      HomeRowKind = "latest_albums"
	// Books: started, then recently added.
	RowReading     HomeRowKind = "reading"
	RowLatestBooks HomeRowKind = "latest_books"
	// Recently added photos.
	RowLatestPhotos HomeRowKind = "latest_photos"
)

// HomeRow is a row of the home page.
type HomeRow struct {
	Kind HomeRowKind
	// Title is a "home...." text. Its params name the library or the source title.
	Title domain.Text
	// Library of a recently added row.
	Library *domain.Library
	// Source is the movie or series of a "Because you watched" row.
	Source *domain.ID
	Items  []domain.ItemView
}

// Home builds the home page of the profile. The server decides which rows there are and in what
// order, and empty rows are left out:
//
//  1. what is in progress: resume, next up, continue reading;
//  2. new things to watch: recently added in movie and series libraries;
//  3. recommendations;
//  4. music: recently played albums, then recently added;
//  5. recently added books, then photos.
//
// Within each group libraries follow their order (store.Libraries). rowSize caps the number of
// items per row (0 means 20).
func (a *App) Home(ctx context.Context, p domain.Principal, rowSize int) ([]HomeRow, error) {
	v, err := viewerOf(p)
	if err != nil {
		return nil, err
	}
	switch {
	case rowSize == 0:
		rowSize = defaultRowSize
	case rowSize < 0 || rowSize > maxRowSize:
		return nil, domain.Invalid("home.invalid_row_size", "max", maxRowSize)
	}
	read := a.store.Read()
	var rows []HomeRow
	add := func(r HomeRow) {
		if len(r.Items) > 0 {
			rows = append(rows, r)
		}
	}
	libs, err := read.Libraries(ctx)
	if err != nil {
		return nil, err
	}
	// latest adds the recently added row of each library of these kinds that the profile can see.
	latest := func(kinds ...domain.LibraryKind) error {
		for _, lib := range libs {
			if !slices.Contains(kinds, lib.Kind) || !v.AllowsLibrary(lib.ID) {
				continue
			}
			row, err := latestRow(ctx, read, v, lib, rowSize)
			if err != nil {
				return err
			}
			add(row)
		}
		return nil
	}

	// 1. In progress.
	resume, err := read.Resume(ctx, v, rowSize)
	if err != nil {
		return nil, err
	}
	add(HomeRow{Kind: RowResume, Title: domain.T("home.resume"), Items: resume})

	next, err := read.NextUp(ctx, v, rowSize)
	if err != nil {
		return nil, err
	}
	// An episode already started is in "Resume": do not list it twice.
	upNext := next[:0]
	for _, v := range next {
		if v.UserData.Position == 0 {
			upNext = append(upNext, v)
		}
	}
	add(HomeRow{Kind: RowNextUp, Title: domain.T("home.next_up"), Items: upNext})

	reading, err := read.ReadingNow(ctx, v, rowSize)
	if err != nil {
		return nil, err
	}
	add(HomeRow{Kind: RowReading, Title: domain.T("home.reading"), Items: reading})

	// 2. New things to watch.
	if err := latest(domain.LibraryMovies, domain.LibraryShows); err != nil {
		return nil, err
	}

	// 3. Recommendations.
	recs, err := a.recommendRows(ctx, v, rowSize)
	if err != nil {
		return nil, err
	}
	for _, r := range recs {
		add(r)
	}

	// 4. Music.
	albums, err := read.RecentAlbums(ctx, v, rowSize)
	if err != nil {
		return nil, err
	}
	add(HomeRow{Kind: RowRecentAlbums, Title: domain.T("home.recent_albums"), Items: albums})
	if err := latest(domain.LibraryMusic); err != nil {
		return nil, err
	}

	// 5. Books, then photos.
	if err := latest(domain.LibraryBooks); err != nil {
		return nil, err
	}
	if err := latest(domain.LibraryPhotos); err != nil {
		return nil, err
	}
	return rows, nil
}

// latestRow is the recently added row of a library.
func latestRow(ctx context.Context, read store.Q, v domain.Viewer, lib domain.Library, rowSize int) (HomeRow, error) {
	row := HomeRow{Library: &lib, Title: domain.T("home.latest", "library", lib.Name)}
	added := func(kind domain.ItemKind) ([]domain.ItemView, error) {
		items, _, err := read.Items(ctx, store.ItemQuery{Kind: kind, LibraryID: &lib.ID, Viewer: v, Sort: domain.SortAdded, Limit: rowSize})
		return items, err
	}
	var err error
	switch lib.Kind {
	case domain.LibraryMovies:
		row.Kind = RowLatestMovies
		row.Items, err = added(domain.ItemMovie)
	case domain.LibraryShows:
		row.Kind = RowLatestSeries
		row.Items, err = read.LatestSeries(ctx, v, lib.ID, rowSize)
	case domain.LibraryMusic:
		row.Kind = RowLatestAlbums
		row.Items, err = added(domain.ItemAlbum)
	case domain.LibraryBooks:
		row.Kind = RowLatestBooks
		row.Items, err = added(domain.ItemBook)
	case domain.LibraryPhotos:
		row.Kind = RowLatestPhotos
		row.Items, err = added(domain.ItemPhoto)
	}
	return row, err
}

// SaveProgress records where the profile is in a movie, an episode or a track, using the thresholds
// of domain.Progress: resume point, nothing to resume, or finished. A track never has a resume
// point: it is either played or not. Playback calls this during a playback and at its end.
func (a *App) SaveProgress(ctx context.Context, p domain.Principal, itemID domain.ID, position time.Duration) error {
	v, err := viewerOf(p)
	if err != nil {
		return err
	}
	profile := v.ProfileID
	if position < 0 {
		return domain.Invalid("request.negative_position")
	}
	read := a.store.Read()
	view, err := read.View(ctx, v, itemID)
	it := view.Item
	if store.IsNotFound(err) || (err == nil && it.Kind != domain.ItemMovie && it.Kind != domain.ItemEpisode && it.Kind != domain.ItemTrack) {
		return domain.NotFound("catalog.item_not_found")
	}
	if err != nil {
		return err
	}
	runtime := it.Runtime
	if runtime == 0 {
		files, err := read.ItemFiles(ctx, itemID)
		if err != nil {
			return err
		}
		for _, f := range files {
			runtime = max(runtime, f.File.Info.Duration)
		}
	}
	resume, finished := domain.Progress(position, runtime)
	if it.Kind == domain.ItemTrack {
		resume = 0
	}
	if err := a.store.Write(ctx, func(q store.Q) error {
		return q.SaveProgress(ctx, profile, itemID, resume, finished, a.now())
	}); err != nil {
		return err
	}
	a.userDataChanged(profile, []domain.ID{itemID})
	return nil
}
