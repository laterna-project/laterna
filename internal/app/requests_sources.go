package app

import (
	"context"
	"maps"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/laterna-project/laterna/internal/arr"
	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/lazylibrarian"
	"github.com/laterna-project/laterna/internal/naming"
	"github.com/laterna-project/laterna/internal/store"
)

// Where requested titles come from: Sonarr (series), Radarr (movies), Lidarr (artists and albums)
// and LazyLibrarian (books). Each family is searched, a title is looked up again when it is
// requested, and each says whether it already follows a title.

func validRequestKind(k domain.RequestKind) error {
	switch k {
	case domain.RequestSeries, domain.RequestMovie, domain.RequestArtist, domain.RequestAlbum, domain.RequestBook:
		return nil
	case domain.RequestMusic: // a family, not something requested
	}
	return domain.Invalid("media_request.invalid_kind")
}

// validRequestFamily checks what a search or a destination is for.
func validRequestFamily(k domain.RequestKind) error {
	switch k {
	case domain.RequestSeries, domain.RequestMovie, domain.RequestMusic, domain.RequestBook:
		return nil
	case domain.RequestArtist, domain.RequestAlbum: // searched and set as music
	}
	return domain.Invalid("media_request.invalid_kind")
}

// requestArr is the instance that handles a kind (or family) of request; books have none.
func requestArr(k domain.RequestKind) arr.Kind {
	switch k.Family() {
	case domain.RequestMovie:
		return arr.Radarr
	case domain.RequestMusic:
		return arr.Lidarr
	case domain.RequestSeries, domain.RequestArtist, domain.RequestAlbum, domain.RequestBook:
	}
	return arr.Sonarr
}

// sourceName is the program that handles a kind of request, as administrators know it.
func sourceName(k domain.RequestKind) string {
	if k.Family() == domain.RequestBook {
		return lazyLibrarianName
	}
	return requestArr(k).Name()
}

// requestClient returns the client of the instance for a kind (not a book). Without it, that kind
// cannot be requested.
func (a *App) requestClient(ctx context.Context, k domain.RequestKind) (*arr.Client, arr.Kind, error) {
	ak := requestArr(k)
	s, ok, err := a.loadIntegration(ctx, string(ak))
	if err != nil {
		return nil, ak, err
	}
	if !ok {
		return nil, ak, domain.Precondition("media_request.unavailable", "name", ak.Name())
	}
	return a.arrClient(ak, s), ak, nil
}

// bookClient returns the client of LazyLibrarian. Without it, books cannot be requested.
func (a *App) bookClient(ctx context.Context) (*lazylibrarian.Client, error) {
	s, ok, err := a.loadIntegration(ctx, lazyLibrarian)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, domain.Precondition("media_request.unavailable", "name", lazyLibrarianName)
	}
	return a.lazyClient(s), nil
}

// instanceError explains a failed call to a source: in full to an administrator, in short to
// anyone else (the details go to the log).
func (a *App) instanceError(ctx context.Context, p domain.Principal, name string, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	a.log.WarnContext(ctx, "requests: instance call failed", "source", name, "err", err)
	if p.CanAdminister() {
		return domain.FromText(domain.ErrPrecondition, integrationProblem(name, err))
	}
	return domain.Precondition("media_request.unavailable", "name", name)
}

// sourceTitle is a title as its source describes it: what a profile sees, its ISBN for a book, and
// whether the source already follows it.
type sourceTitle struct {
	domain.RequestableTitle
	ISBN    string
	Tracked bool
}

// searchSource looks titles up in the source of a family, best match first.
func (a *App) searchSource(ctx context.Context, p domain.Principal, family domain.RequestKind, query string) ([]sourceTitle, error) {
	if family == domain.RequestBook {
		client, err := a.bookClient(ctx)
		if err != nil {
			return nil, err
		}
		found, err := client.Search(ctx, query)
		if err != nil {
			return nil, a.instanceError(ctx, p, lazyLibrarianName, err)
		}
		found = found[:min(len(found), maxRequestResults)]
		status, err := a.bookStatus(ctx, client)
		if err != nil {
			return nil, a.instanceError(ctx, p, lazyLibrarianName, err)
		}
		out := make([]sourceTitle, len(found))
		for i, b := range found {
			a.books.remember(b, a.now())
			out[i] = requestableBook(b)
			if st, ok := status[b.ID]; ok {
				out[i].Tracked = st.Wanted() || st.InLibrary()
			}
		}
		return out, nil
	}
	client, ak, err := a.requestClient(ctx, family)
	if err != nil {
		return nil, err
	}
	if family == domain.RequestMusic {
		found, err := client.SearchMusic(ctx, query)
		if err != nil {
			return nil, a.instanceError(ctx, p, ak.Name(), err)
		}
		out := make([]sourceTitle, 0, len(found))
		for _, t := range found {
			if t.MBID != "" {
				out = append(out, musicTitle(t))
			}
		}
		return out, nil
	}
	found, err := client.Search(ctx, query)
	if err != nil {
		return nil, a.instanceError(ctx, p, ak.Name(), err)
	}
	out := make([]sourceTitle, 0, len(found))
	for _, t := range found {
		// A result without an ID (not on TVDB or TMDB yet) cannot be requested.
		if t.ExternalID > 0 {
			out = append(out, videoTitle(family, t))
		}
	}
	return out, nil
}

// findSource looks a requested title up again in its source; false if it does not know it.
func (a *App) findSource(ctx context.Context, p domain.Principal, kind domain.RequestKind, id int64, key string) (sourceTitle, bool, error) {
	switch kind {
	case domain.RequestBook:
		client, err := a.bookClient(ctx)
		if err != nil {
			return sourceTitle{}, false, err
		}
		status, err := a.bookStatus(ctx, client)
		if err != nil {
			return sourceTitle{}, false, a.instanceError(ctx, p, lazyLibrarianName, err)
		}
		// LazyLibrarian cannot look a book up by its ID: the book comes from a recent search, or
		// from the books it knows.
		b, ok := a.books.find(key, a.now())
		st, known := status[key]
		if !ok && !known {
			return sourceTitle{}, false, nil
		}
		if !ok {
			b = st
		}
		t := requestableBook(b)
		t.Tracked = known && (st.Wanted() || st.InLibrary())
		return t, true, nil
	case domain.RequestArtist, domain.RequestAlbum:
		client, ak, err := a.requestClient(ctx, kind)
		if err != nil {
			return sourceTitle{}, false, err
		}
		t, ok, err := client.FindMusic(ctx, kind == domain.RequestAlbum, key)
		if err != nil {
			return sourceTitle{}, false, a.instanceError(ctx, p, ak.Name(), err)
		}
		return musicTitle(t), ok, nil
	case domain.RequestSeries, domain.RequestMovie, domain.RequestMusic:
	}
	client, ak, err := a.requestClient(ctx, kind)
	if err != nil {
		return sourceTitle{}, false, err
	}
	t, ok, err := client.Find(ctx, id)
	if err != nil {
		return sourceTitle{}, false, a.instanceError(ctx, p, ak.Name(), err)
	}
	return videoTitle(kind, t), ok, nil
}

func videoTitle(kind domain.RequestKind, t arr.Title) sourceTitle {
	return sourceTitle{
		RequestableTitle: domain.RequestableTitle{
			Kind: kind, ExternalID: t.ExternalID, Title: t.Title, Year: t.Year, Overview: t.Overview, Poster: t.Poster,
			Network: t.Network, SeasonCount: len(t.Seasons),
		},
		Tracked: t.ArrID > 0 && t.Monitored,
	}
}

func musicTitle(t arr.MusicTitle) sourceTitle {
	kind := domain.RequestArtist
	if t.Album {
		kind = domain.RequestAlbum
	}
	return sourceTitle{
		RequestableTitle: domain.RequestableTitle{
			Kind: kind, ExternalKey: t.MBID, Title: t.Title, Year: t.Year, Overview: t.Overview, Poster: t.Poster,
			Network: t.Artist,
		},
		Tracked: t.ArrID > 0 && t.Monitored,
	}
}

func requestableBook(b lazylibrarian.Book) sourceTitle {
	return sourceTitle{
		RequestableTitle: domain.RequestableTitle{
			Kind: domain.RequestBook, ExternalKey: b.ID, Title: b.Title, Year: b.Year, Overview: b.Overview, Poster: b.Cover,
			Network: b.Author,
		},
		ISBN: b.ISBN,
	}
}

// bookStatus reads the books LazyLibrarian knows, by ID.
func (a *App) bookStatus(ctx context.Context, client *lazylibrarian.Client) (map[string]lazylibrarian.Book, error) {
	books, err := client.Books(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]lazylibrarian.Book, len(books))
	for _, b := range books {
		out[b.ID] = b
	}
	return out, nil
}

// inCatalog says which titles of a kind the catalog has (present), and the item of each that the
// profile sees (visible), by title key (domain.RequestableTitle.Key).
func (a *App) inCatalog(ctx context.Context, p domain.Principal, kind domain.RequestKind, titles []sourceTitle) (visible map[string]domain.ID, present map[string]bool, err error) {
	read := a.store.Read()
	items := map[string][]store.ExternalItem{}
	if kind == domain.RequestBook {
		books, err := read.CatalogBooks(ctx)
		if err != nil {
			return nil, nil, err
		}
		for _, t := range titles {
			if found := matchBooks(books, t.Title, t.Network, t.ISBN); len(found) > 0 {
				items[t.Key()] = found
			}
		}
	} else if len(titles) > 0 {
		keys := make([]string, len(titles))
		for i, t := range titles {
			keys[i] = t.Key()
		}
		if items, err = read.ItemsWithExternalIDs(ctx, kind.ExternalProvider(), keys); err != nil {
			return nil, nil, err
		}
	}
	visible, present = map[string]domain.ID{}, map[string]bool{}
	var itemIDs []domain.ID
	for key, list := range items {
		present[key] = true
		for _, it := range list {
			itemIDs = append(itemIDs, it.ItemID)
		}
	}
	v, ok := p.Viewer()
	if !ok || len(itemIDs) == 0 {
		return visible, present, nil
	}
	views, err := read.ViewsByID(ctx, v, itemIDs)
	if err != nil {
		return nil, nil, err
	}
	for key, list := range items {
		for _, it := range list {
			if _, ok := views[it.ItemID]; ok {
				visible[key] = it.ItemID
				break
			}
		}
	}
	return visible, present, nil
}

// matchBooks finds a book of the catalog by its ISBN, or by its title and one of its authors (the
// title alone if the catalog does not know them).
func matchBooks(books []store.CatalogBook, title, author, isbn string) []store.ExternalItem {
	var out []store.ExternalItem
	wantISBN, wantTitle, wantAuthor := isbnDigits(isbn), naming.Key(title), naming.Key(author)
	for _, b := range books {
		byISBN := wantISBN != "" && isbnDigits(b.ISBN) == wantISBN
		byTitle := wantTitle != "" && naming.Key(b.Title) == wantTitle && authorMatches(b.Authors, wantAuthor)
		if byISBN || byTitle {
			out = append(out, store.ExternalItem{ItemID: b.ItemID, LibraryID: b.LibraryID})
		}
	}
	return out
}

func authorMatches(authors []string, want string) bool {
	if want == "" || len(authors) == 0 {
		return true
	}
	for _, a := range authors {
		if k := naming.Key(a); k == want || strings.Contains(k, want) || strings.Contains(want, k) {
			return true
		}
	}
	return false
}

// isbnDigits keeps the digits (and a final X) of an ISBN.
func isbnDigits(isbn string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsDigit(r) || r == 'X' || r == 'x' {
			return unicode.ToUpper(r)
		}
		return -1
	}, isbn)
}

// Books found by a search, kept for an hour: LazyLibrarian cannot look a book up by its ID before
// it adds it, and a book is requested from a search.

const maxRememberedBooks = 10_000

type bookCache struct {
	mu   sync.Mutex
	byID map[string]rememberedBook
}

type rememberedBook struct {
	book lazylibrarian.Book
	seen time.Time
}

func (c *bookCache) remember(b lazylibrarian.Book, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.byID == nil {
		c.byID = map[string]rememberedBook{}
	}
	if len(c.byID) >= maxRememberedBooks {
		maps.DeleteFunc(c.byID, func(_ string, r rememberedBook) bool { return now.Sub(r.seen) > posterTTL })
		if len(c.byID) >= maxRememberedBooks {
			return
		}
	}
	c.byID[b.ID] = rememberedBook{book: b, seen: now}
}

func (c *bookCache) find(id string, now time.Time) (lazylibrarian.Book, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.byID[id]
	if !ok || now.Sub(r.seen) > posterTTL {
		return lazylibrarian.Book{}, false
	}
	return r.book, true
}
