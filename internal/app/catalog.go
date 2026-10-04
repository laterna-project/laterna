package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/store"
)

// Page and search sizes.
const (
	defaultPageSize    = 50
	maxPageSize        = 200
	defaultSearchLimit = 20
	maxSearchLimit     = 50
	maxSearchLen       = 200
)

// ListQuery describes a page of a catalog list (movies, series, artists, albums, tracks).
type ListQuery struct {
	// LibraryID restricts to one library; nil means all.
	LibraryID *domain.ID
	// ArtistID restricts albums to those of an artist, SeriesID books to those of a series; nil
	// means all.
	ArtistID *domain.ID
	SeriesID *domain.ID
	// Sort: "" means by title.
	Sort    domain.ItemSort
	Reverse bool
	Genre   string
	// Played filters on the played state (movies only).
	Played        *bool
	FavoritesOnly bool
	PageSize      int
	PageToken     string
}

// ListPage is a page of a catalog list.
type ListPage struct {
	Items []domain.ItemView
	// NextPageToken is empty on the last page.
	NextPageToken string
	// Total counts the items of all pages.
	Total int
}

// Details completes the view of an item on its details page.
type Details struct {
	Genres      []string
	Studios     []string
	ProviderIDs map[string]string
	Credits     []domain.Credit
	// Collections a movie or a series belongs to.
	Collections []domain.Collection
	// Files of a movie or an episode, by version then part.
	Files []FileVersion
}

// FileVersion is a file of an item, with its version ("4K") and part (CD1...).
type FileVersion struct {
	File    domain.MediaFile
	Version string
	Part    int
	// Trickplay holds the scrubbing thumbnails, nil until they are ready.
	Trickplay *domain.Trickplay
	// Book says how to read a book file, nil until it is analyzed.
	Book *domain.BookFile
}

// pageToken is what a page token holds: the order of the list (a token only works for the list that
// produced it) and the cursor. Opaque to the client.
type pageToken struct {
	Sort    domain.ItemSort `json:"s"`
	Reverse bool            `json:"r,omitempty"`
	store.Cursor
}

// viewerOf returns the profile the caller picked and what it is allowed to see. The whole catalog
// is seen through it, and what it may not see cannot be found.
func viewerOf(p domain.Principal) (domain.Viewer, error) {
	v, ok := p.Viewer()
	if !ok {
		return domain.Viewer{}, domain.Precondition("profile.required")
	}
	return v, nil
}

// CatalogLibraries lists the libraries the caller can browse, with the number of movies, series,
// episodes, artists, albums and tracks they can see there.
func (a *App) CatalogLibraries(ctx context.Context, p domain.Principal) ([]LibraryInfo, error) {
	v, err := viewerOf(p)
	if err != nil {
		return nil, err
	}
	read := a.store.Read()
	libs, err := read.Libraries(ctx)
	if err != nil {
		return nil, err
	}
	var out []LibraryInfo
	for _, l := range libs {
		if !v.AllowsLibrary(l.ID) {
			continue
		}
		info := LibraryInfo{Library: l, Counts: map[domain.ItemKind]int{}}
		for _, kind := range []domain.ItemKind{
			domain.ItemMovie, domain.ItemSeries, domain.ItemEpisode, domain.ItemArtist, domain.ItemAlbum, domain.ItemTrack,
		} {
			n, err := read.CountItems(ctx, store.ItemQuery{Kind: kind, LibraryID: &l.ID, Viewer: v})
			if err != nil {
				return nil, err
			}
			if n > 0 {
				info.Counts[kind] = n
			}
		}
		out = append(out, info)
	}
	return out, nil
}

// visible checks that the caller is allowed to see an item (not found otherwise).
func (a *App) visible(ctx context.Context, v domain.Viewer, id domain.ID) (domain.ItemView, error) {
	view, err := a.store.Read().View(ctx, v, id)
	if store.IsNotFound(err) {
		return view, domain.NotFound("catalog.item_not_found")
	}
	return view, err
}

// ListMovies returns a page of movies.
func (a *App) ListMovies(ctx context.Context, p domain.Principal, lq ListQuery) (ListPage, error) {
	return a.list(ctx, p, domain.ItemMovie, lq)
}

// ListSeries returns a page of series.
func (a *App) ListSeries(ctx context.Context, p domain.Principal, lq ListQuery) (ListPage, error) {
	if lq.Played != nil {
		return ListPage{}, domain.Invalid("catalog.played_filter_unsupported")
	}
	return a.list(ctx, p, domain.ItemSeries, lq)
}

// ListArtists returns a page of artists (those with at least one album).
func (a *App) ListArtists(ctx context.Context, p domain.Principal, lq ListQuery) (ListPage, error) {
	if lq.Played != nil {
		return ListPage{}, domain.Invalid("catalog.played_filter_unsupported")
	}
	return a.list(ctx, p, domain.ItemArtist, lq)
}

// ListAlbums returns a page of albums, optionally those of one artist.
func (a *App) ListAlbums(ctx context.Context, p domain.Principal, lq ListQuery) (ListPage, error) {
	if lq.Played != nil {
		return ListPage{}, domain.Invalid("catalog.played_filter_unsupported")
	}
	if lq.ArtistID != nil {
		if err := a.checkKind(ctx, *lq.ArtistID, domain.ItemArtist); err != nil {
			return ListPage{}, err
		}
	}
	return a.list(ctx, p, domain.ItemAlbum, lq)
}

// ListTracks returns a page of tracks (all of them, or the favorites).
func (a *App) ListTracks(ctx context.Context, p domain.Principal, lq ListQuery) (ListPage, error) {
	return a.list(ctx, p, domain.ItemTrack, lq)
}

func (a *App) list(ctx context.Context, p domain.Principal, kind domain.ItemKind, lq ListQuery) (ListPage, error) {
	v, err := viewerOf(p)
	if err != nil {
		return ListPage{}, err
	}
	if lq.Sort == "" {
		lq.Sort = domain.SortTitle
	}
	if !lq.Sort.Valid() {
		return ListPage{}, domain.Invalid("catalog.unknown_sort")
	}
	size := lq.PageSize
	switch {
	case size == 0:
		size = defaultPageSize
	case size < 0 || size > maxPageSize:
		return ListPage{}, domain.Invalid("request.invalid_page_size", "max", maxPageSize)
	}
	parent := lq.ArtistID
	if lq.SeriesID != nil {
		parent = lq.SeriesID
	}
	iq := store.ItemQuery{
		Kind: kind, LibraryID: lq.LibraryID, ParentID: parent, Viewer: v, Genre: strings.TrimSpace(lq.Genre),
		Played: lq.Played, Favorite: lq.FavoritesOnly, Sort: lq.Sort, Reverse: lq.Reverse, Limit: size,
	}
	if lq.PageToken != "" {
		tok, err := decodePageToken(lq.PageToken)
		if err != nil || tok.Sort != lq.Sort || tok.Reverse != lq.Reverse {
			return ListPage{}, domain.Invalid("request.invalid_page_token")
		}
		iq.After = &tok.Cursor
	}
	read := a.store.Read()
	if lq.LibraryID != nil {
		if _, err := read.Library(ctx, *lq.LibraryID); store.IsNotFound(err) || (err == nil && !v.AllowsLibrary(*lq.LibraryID)) {
			return ListPage{}, domain.NotFound("library.not_found")
		} else if err != nil {
			return ListPage{}, err
		}
	}
	items, next, err := read.Items(ctx, iq)
	if err != nil {
		return ListPage{}, err
	}
	total, err := read.CountItems(ctx, iq)
	if err != nil {
		return ListPage{}, err
	}
	page := ListPage{Items: items, Total: total}
	if next != nil {
		page.NextPageToken = encodePageToken(pageToken{Sort: lq.Sort, Reverse: lq.Reverse, Cursor: *next})
	}
	return page, nil
}

func encodePageToken(t pageToken) string {
	b, _ := json.Marshal(t) // strings, a boolean, a finite number and an ID: cannot fail
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodePageToken(s string) (pageToken, error) {
	var t pageToken
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return t, err
	}
	return t, json.Unmarshal(b, &t)
}

// Movie returns the details of a movie.
func (a *App) Movie(ctx context.Context, p domain.Principal, id domain.ID) (domain.ItemView, Details, error) {
	return a.itemWithDetails(ctx, p, id, domain.ItemMovie, true)
}

// Episode returns the details of an episode.
func (a *App) Episode(ctx context.Context, p domain.Principal, id domain.ID) (domain.ItemView, Details, error) {
	return a.itemWithDetails(ctx, p, id, domain.ItemEpisode, true)
}

// Series returns the details of a series and its seasons.
func (a *App) Series(ctx context.Context, p domain.Principal, id domain.ID) (domain.ItemView, Details, []domain.ItemView, error) {
	view, details, err := a.itemWithDetails(ctx, p, id, domain.ItemSeries, false)
	if err != nil {
		return view, details, nil, err
	}
	v, err := viewerOf(p)
	if err != nil {
		return view, details, nil, err
	}
	seasons, err := a.store.Read().Seasons(ctx, v, id)
	return view, details, seasons, err
}

// Episodes lists the present episodes of a series, or of one of its seasons.
func (a *App) Episodes(ctx context.Context, p domain.Principal, seriesID domain.ID, seasonID *domain.ID) ([]domain.ItemView, error) {
	v, err := viewerOf(p)
	if err != nil {
		return nil, err
	}
	read := a.store.Read()
	if err := a.checkKind(ctx, seriesID, domain.ItemSeries); err != nil {
		return nil, err
	}
	if _, err := read.View(ctx, v, seriesID); store.IsNotFound(err) {
		return nil, domain.NotFound("catalog.item_not_found", "kind", domain.ItemSeries)
	} else if err != nil {
		return nil, err
	}
	if seasonID != nil {
		season, err := read.Season(ctx, *seasonID)
		if store.IsNotFound(err) || (err == nil && season.SeriesID != seriesID) {
			return nil, domain.NotFound("catalog.item_not_found", "kind", domain.ItemSeason)
		}
		if err != nil {
			return nil, err
		}
	}
	return read.Episodes(ctx, v, seriesID, seasonID)
}

// Artist returns the details of an artist and their albums, newest first.
func (a *App) Artist(ctx context.Context, p domain.Principal, id domain.ID) (domain.ItemView, Details, []domain.ItemView, error) {
	view, details, err := a.itemWithDetails(ctx, p, id, domain.ItemArtist, false)
	if err != nil {
		return view, details, nil, err
	}
	v, err := viewerOf(p)
	if err != nil {
		return view, details, nil, err
	}
	albums, _, err := a.store.Read().Items(ctx, store.ItemQuery{
		Kind: domain.ItemAlbum, ParentID: &id, Viewer: v, Sort: domain.SortReleased, Limit: maxPageSize,
	})
	return view, details, albums, err
}

// Album returns the details of an album and its tracks, in disc order.
func (a *App) Album(ctx context.Context, p domain.Principal, id domain.ID) (domain.ItemView, Details, []domain.ItemView, error) {
	view, details, err := a.itemWithDetails(ctx, p, id, domain.ItemAlbum, false)
	if err != nil {
		return view, details, nil, err
	}
	v, err := viewerOf(p)
	if err != nil {
		return view, details, nil, err
	}
	tracks, err := a.store.Read().Tracks(ctx, v, view.Item)
	return view, details, tracks, err
}

// Track returns the details of a track and its files.
func (a *App) Track(ctx context.Context, p domain.Principal, id domain.ID) (domain.ItemView, Details, error) {
	return a.itemWithDetails(ctx, p, id, domain.ItemTrack, true)
}

// ArtistTracks lists the present tracks of all the albums of an artist, album by album (to play
// everything in one go).
func (a *App) ArtistTracks(ctx context.Context, p domain.Principal, artistID domain.ID) ([]domain.ItemView, error) {
	v, err := viewerOf(p)
	if err != nil {
		return nil, err
	}
	if err := a.checkKind(ctx, artistID, domain.ItemArtist); err != nil {
		return nil, err
	}
	view, err := a.visible(ctx, v, artistID)
	if err != nil {
		return nil, err
	}
	return a.store.Read().Tracks(ctx, v, view.Item)
}

// checkKind checks that an item exists and is of the expected kind: a series ID passed as a movie
// is simply "not found".
func (a *App) checkKind(ctx context.Context, id domain.ID, kind domain.ItemKind) error {
	it, err := a.store.Read().Item(ctx, id)
	if store.IsNotFound(err) || (err == nil && it.Kind != kind) {
		return domain.NotFound("catalog.item_not_found", "kind", kind)
	}
	return err
}

func (a *App) itemWithDetails(ctx context.Context, p domain.Principal, id domain.ID, kind domain.ItemKind, files bool) (domain.ItemView, Details, error) {
	v, err := viewerOf(p)
	if err != nil {
		return domain.ItemView{}, Details{}, err
	}
	if err := a.checkKind(ctx, id, kind); err != nil {
		return domain.ItemView{}, Details{}, err
	}
	read := a.store.Read()
	view, err := read.View(ctx, v, id)
	if store.IsNotFound(err) {
		return domain.ItemView{}, Details{}, domain.NotFound("catalog.item_not_found", "kind", kind)
	}
	if err != nil {
		return domain.ItemView{}, Details{}, err
	}
	d, err := read.Details(ctx, id)
	if err != nil {
		return domain.ItemView{}, Details{}, err
	}
	out := Details{Genres: d.Genres, Studios: d.Studios, ProviderIDs: d.ProviderIDs, Credits: d.Credits}
	if kind == domain.ItemMovie || kind == domain.ItemSeries {
		if out.Collections, err = read.ItemCollections(ctx, id); err != nil {
			return domain.ItemView{}, Details{}, err
		}
	}
	if files {
		list, err := read.ItemFiles(ctx, id)
		if err != nil {
			return domain.ItemView{}, Details{}, err
		}
		for _, f := range list {
			fv := FileVersion{File: f.File, Version: f.Version, Part: f.Part}
			if kind == domain.ItemBook {
				bf, err := read.BookFile(ctx, f.File.ID)
				if err != nil && !store.IsNotFound(err) {
					return domain.ItemView{}, Details{}, err
				}
				if err == nil {
					fv.Book = &bf
				}
			} else if fv.Trickplay, err = a.trickplayOf(ctx, f.File); err != nil {
				return domain.ItemView{}, Details{}, err
			}
			out.Files = append(out.Files, fv)
		}
	}
	return view, out, nil
}

// Genres lists the genres of the movies and series the caller can see in a library (all of them if
// libraryID is nil).
func (a *App) Genres(ctx context.Context, p domain.Principal, libraryID *domain.ID) ([]domain.GenreCount, error) {
	v, err := viewerOf(p)
	if err != nil {
		return nil, err
	}
	return a.store.Read().Genres(ctx, v, libraryID)
}

// Search looks up movies, series, episodes, artists, albums and tracks by title.
func (a *App) Search(ctx context.Context, p domain.Principal, text string, limit int) ([]domain.ItemView, error) {
	v, err := viewerOf(p)
	if err != nil {
		return nil, err
	}
	switch {
	case limit == 0:
		limit = defaultSearchLimit
	case limit < 0 || limit > maxSearchLimit:
		return nil, domain.Invalid("request.invalid_limit", "max", maxSearchLimit)
	}
	if utf8.RuneCountInString(text) > maxSearchLen {
		return nil, domain.Invalid("catalog.search_too_long", "max", maxSearchLen)
	}
	return a.store.Read().Search(ctx, v, text, limit)
}

// SetPlayed marks an item as played or unplayed for the profile. For a series or a season that
// means all its episodes; for an artist or an album, all its tracks; for a book series, all its
// books. A book marked unread is read again from the start.
func (a *App) SetPlayed(ctx context.Context, p domain.Principal, itemID domain.ID, played bool) error {
	v, err := viewerOf(p)
	if err != nil {
		return err
	}
	view, err := a.visible(ctx, v, itemID)
	if err != nil {
		return err
	}
	if k := view.Item.Kind; k == domain.ItemPhoto || k == domain.ItemPhotoAlbum {
		return domain.Invalid("catalog.photo_not_playable")
	}
	profile := v.ProfileID
	var ids []domain.ID
	err = a.store.Write(ctx, func(q store.Q) error {
		it := view.Item
		ids = []domain.ID{it.ID}
		var err error
		switch it.Kind {
		case domain.ItemSeries, domain.ItemSeason:
			ids, err = q.EpisodeIDs(ctx, it)
		case domain.ItemAlbum:
			ids, err = q.AlbumTrackIDs(ctx, it.ID)
		case domain.ItemArtist:
			ids, err = q.ArtistTrackIDs(ctx, it.ID)
		case domain.ItemBookSeries:
			ids, err = q.SeriesBookIDs(ctx, it.ID)
		case domain.ItemMovie, domain.ItemEpisode, domain.ItemTrack, domain.ItemBook, domain.ItemPhotoAlbum, domain.ItemPhoto:
		}
		if err != nil {
			return err
		}
		if !played && (it.Kind == domain.ItemBook || it.Kind == domain.ItemBookSeries) {
			for _, id := range ids {
				if err := q.ClearReadingProgress(ctx, profile, id); err != nil {
					return err
				}
			}
		}
		return q.SetPlayed(ctx, profile, ids, played, a.now())
	})
	if err == nil {
		a.userDataChanged(profile, ids)
	}
	return err
}

// SetFavorite adds an item to the profile's favorites or removes it.
func (a *App) SetFavorite(ctx context.Context, p domain.Principal, itemID domain.ID, favorite bool) error {
	v, err := viewerOf(p)
	if err != nil {
		return err
	}
	if _, err := a.visible(ctx, v, itemID); err != nil {
		return err
	}
	profile := v.ProfileID
	err = a.store.Write(ctx, func(q store.Q) error {
		return q.SetFavorite(ctx, profile, itemID, favorite, a.now())
	})
	if err == nil {
		a.userDataChanged(profile, []domain.ID{itemID})
	}
	return err
}
