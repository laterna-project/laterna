package app

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/jobs"
	"github.com/laterna-project/laterna/internal/media/books"
	"github.com/laterna-project/laterna/internal/media/images"
	"github.com/laterna-project/laterna/internal/metadata"
	"github.com/laterna-project/laterna/internal/naming"
	"github.com/laterna-project/laterna/internal/store"
)

// Books: series and books, described by their name, then by what the files say (the OPF of an EPUB,
// ComicInfo.xml, the Info of a PDF), then by the metadata.opf next to them (Calibre library). A
// book is read page by page (images served one at a time, resized) or from the whole file (EPUB,
// text PDF), rendered by the client.

// bookTypes is the content type of each format, to serve the whole file.
var bookTypes = map[domain.BookFormat]string{
	domain.BookEPUB: "application/epub+zip", domain.BookPDF: "application/pdf", domain.BookCBZ: "application/vnd.comicbook+zip",
}

// bookMeta gathers what is known about a book file: its name, overridden by what the file says,
// overridden by the metadata.opf next to it if the book is alone in its folder.
func bookMeta(filePath, rel string, inFile metadata.BookMeta) metadata.BookMeta {
	names := naming.ParseBook(rel)
	m := metadata.BookMeta{Title: names.Title, Series: names.Series, Number: names.Number, HasNumber: names.HasNumber}
	if names.Year > 0 {
		m.Date = strconv.Itoa(names.Year)
	}
	m = m.Merge(inFile)
	if aloneInDir(filePath) {
		if f, err := os.Open(filepath.Join(filepath.Dir(filePath), "metadata.opf")); err == nil {
			opf, err := metadata.ParseOPF(f)
			_ = f.Close()
			if err == nil {
				m = m.Merge(opf.Meta)
			}
		}
	}
	return m
}

// aloneInDir reports a book that is alone in its folder (its other formats aside): what sits next
// to it (metadata.opf, cover.jpg) describes it.
func aloneInDir(filePath string) bool {
	entries, err := os.ReadDir(filepath.Dir(filePath))
	if err != nil {
		return false
	}
	base := strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath))
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() && naming.IsBook(name) && strings.TrimSuffix(name, filepath.Ext(name)) != base {
			return false
		}
	}
	return true
}

// bookTitle is the title of a book: its own, otherwise that of an untitled volume
// (domain.BookTitle), otherwise the file name.
func bookTitle(m metadata.BookMeta, rel string) string {
	switch {
	case m.Title != "":
		return m.Title
	case m.Series != "" && m.HasNumber:
		return domain.BookTitle(m.Series, m.Number)
	case m.Series != "":
		return m.Series
	}
	return strings.TrimSuffix(path.Base(rel), path.Ext(rel))
}

func numberText(n float64) string { return strconv.FormatFloat(n, 'f', -1, 64) }

// bookSeriesKey is the group key of a book's series; empty for a standalone book.
func bookSeriesKey(m metadata.BookMeta) string {
	if m.Series == "" {
		return ""
	}
	return "bookseries:" + musicKey(m.Series)
}

// bookKey is the group key of a book: same series and same volume, or same folder and same title
// (an EPUB and a PDF of the same book).
func bookKey(m metadata.BookMeta, rel string, seriesID *domain.ID) string {
	switch {
	case seriesID != nil && m.HasNumber:
		return fmt.Sprintf("book:%s:%s", *seriesID, numberText(m.Number))
	case seriesID != nil:
		return fmt.Sprintf("book:%s:%s", *seriesID, musicKey(bookTitle(m, rel)))
	}
	return "book:" + path.Dir(rel) + ":" + musicKey(bookTitle(m, rel))
}

// analyzeBook analyzes a book file and files it: series, book, how to read it.
func (a *App) analyzeBook(ctx context.Context, lib domain.Library, f domain.MediaFile, rel string) error {
	info, readErr := books.Read(f.Path)
	now := a.now()
	if readErr != nil {
		_ = a.store.Write(ctx, func(q store.Q) error { return q.SetFileAnalysisError(ctx, f.ID, readErr.Error(), now) })
		return jobs.Permanent(readErr)
	}
	m := bookMeta(f.Path, rel, info.Meta)
	// A new analysis means a new key: pages of previous content are no longer served.
	if err := os.RemoveAll(a.bookCacheDir(f.ID)); err != nil {
		return err
	}
	var enrich []domain.ID
	var removed int64
	err := a.store.Write(ctx, func(q store.Q) error {
		if err := q.SetFileAnalysis(ctx, f.ID, domain.MediaInfo{Container: string(info.Format)}, now); err != nil {
			return err
		}
		var err error
		enrich, err = a.placeBook(ctx, q, lib, f, rel, m, info)
		if err != nil {
			return err
		}
		for _, id := range enrich {
			if err := a.jobs.Enqueue(ctx, q, jobItemMetadata, id.String(), priorityBackground); err != nil {
				return err
			}
		}
		removed, err = q.DeleteOrphanItems(ctx, lib.ID)
		return err
	})
	if err != nil {
		return err
	}
	a.jobs.Kick()
	a.itemsChanged(lib.ID, enrich...)
	if removed > 0 {
		a.libraryChanged(lib.ID)
	}
	return nil
}

// placeBook files a book file under its series (created if needed) and its book. It returns the
// items whose metadata must be read again.
func (a *App) placeBook(ctx context.Context, q store.Q, lib domain.Library, f domain.MediaFile, rel string, m metadata.BookMeta, info books.Info) ([]domain.ID, error) {
	var enrich []domain.ID
	var seriesID *domain.ID
	if k := bookSeriesKey(m); k != "" {
		series, _, err := a.findOrCreate(ctx, q, domain.Item{LibraryID: lib.ID, Kind: domain.ItemBookSeries, GroupKey: k, Title: m.Series})
		if err != nil {
			return nil, err
		}
		seriesID = &series.ID
		// Its cover follows that of its first volume.
		enrich = append(enrich, series.ID)
	}
	key := bookKey(m, rel, seriesID)
	if err := a.followMove(ctx, q, lib.ID, f.ID, key, false); err != nil {
		return nil, err
	}
	title := bookTitle(m, rel)
	sortTitle := naming.SortTitle(firstNonEmpty(m.SortTitle, title))
	book, _, err := a.findOrCreate(ctx, q, domain.Item{
		LibraryID: lib.ID, Kind: domain.ItemBook, ParentID: seriesID, GroupKey: key, Title: title, SortTitle: sortTitle, Year: m.Year(),
	})
	if err != nil {
		return nil, err
	}
	if err := a.reparent(ctx, q, book, seriesID); err != nil {
		return nil, err
	}
	if err := q.SetBook(ctx, domain.Book{ItemID: book.ID, Number: m.Number, Publisher: m.Publisher, Language: m.Language, ISBN: m.ISBN}); err != nil {
		return nil, err
	}
	if err := q.SetBookFile(ctx, domain.BookFile{
		FileID: f.ID, Format: info.Format, Layout: info.Layout, RightToLeft: info.RightToLeft || m.RightToLeft,
		PageCount: info.PageCount, Pages: info.Pages, Key: randomKey(),
	}); err != nil {
		return nil, err
	}
	// Two formats of the same book (EPUB and PDF) are versions told apart by their format.
	if err := q.LinkFile(ctx, book.ID, f.ID, strings.ToUpper(string(info.Format)), 0); err != nil {
		return nil, err
	}
	return append(enrich, book.ID), nil
}

// refreshBook reads the metadata of a book again (after its analysis, or when the scan sees a
// change in what sits next to it), and its cover.
func (a *App) refreshBook(ctx context.Context, lib domain.Library, item domain.Item) error {
	read := a.store.Read()
	files, err := read.ItemFiles(ctx, item.ID)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(files, func(f store.ItemFile) bool { return f.File.MissingSince == nil })
	if i < 0 {
		return nil
	}
	f := files[i].File
	_, rel, ok := relativeTo(lib.Paths, f.Path)
	if !ok {
		return nil
	}
	inFile, err := books.ReadMeta(f.Path)
	if err != nil {
		a.log.WarnContext(ctx, "book: unreadable file metadata", "path", f.Path, "err", err)
	}
	m := bookMeta(f.Path, rel, inFile)
	// The series changed (metadata.opf edited): the file is filed again.
	current := ""
	if item.ParentID != nil {
		if parent, err := read.Item(ctx, *item.ParentID); err == nil {
			current = parent.GroupKey
		}
	}
	if bookSeriesKey(m) != current {
		err := a.store.Write(ctx, func(q store.Q) error {
			return a.jobs.Enqueue(ctx, q, jobAnalyzeFile, f.ID.String(), priorityBackground)
		})
		a.jobs.Kick()
		return err
	}

	title := bookTitle(m, rel)
	meta := domain.Metadata{
		Title: title, SortTitle: naming.SortTitle(firstNonEmpty(m.SortTitle, title)), Year: m.Year(),
		Overview: m.Description, Genres: m.Genres,
	}
	if len(m.Date) == len("2006-01-02") {
		meta.PremiereDate = m.Date
	}
	if m.ISBN != "" {
		meta.ProviderIDs = map[string]string{"isbn": m.ISBN}
	}
	for i, name := range m.Authors {
		meta.Credits = append(meta.Credits, domain.Credit{Name: name, Role: domain.RoleWriter, Order: i})
	}
	for i, name := range m.Illustrators {
		meta.Credits = append(meta.Credits, domain.Credit{Name: name, Role: domain.RoleIllustrator, Order: i})
	}
	base := strings.TrimSuffix(filepath.Base(f.Path), filepath.Ext(f.Path))
	art, err := metadata.BookArtwork(filepath.Dir(f.Path), base, aloneInDir(f.Path))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	var embedded []wantedImage
	if len(art) == 0 {
		if w, ok := a.bookCover(ctx, item.ID, f); ok {
			embedded = append(embedded, w)
		}
	}
	want := a.wantedImages(item.ID, art, embedded, nil)
	err = a.store.Write(ctx, func(q store.Q) error {
		people, err := q.SetMetadata(ctx, item.ID, meta, a.now())
		if err != nil {
			return err
		}
		if err := q.SetBook(ctx, domain.Book{ItemID: item.ID, Number: m.Number, Publisher: m.Publisher, Language: m.Language, ISBN: m.ISBN}); err != nil {
			return err
		}
		if err := a.syncImages(ctx, q, item.ID, want); err != nil {
			return err
		}
		if err := a.setPeopleImages(ctx, q, meta.Credits, people); err != nil {
			return err
		}
		// Its series takes the cover of its first volume.
		if item.ParentID != nil {
			return a.jobs.Enqueue(ctx, q, jobItemMetadata, item.ParentID.String(), priorityBackground)
		}
		return nil
	})
	if err != nil {
		return err
	}
	a.jobs.Kick()
	a.itemsChanged(item.LibraryID, item.ID)
	return nil
}

// refreshBookSeries gives a book series the cover of its first present volume.
func (a *App) refreshBookSeries(ctx context.Context, item domain.Item) error {
	read := a.store.Read()
	var want []wantedImage
	first, err := read.FirstBookOfSeries(ctx, item.ID)
	switch {
	case err == nil:
		imgs, err := read.ItemImages(ctx, first)
		if err != nil {
			return err
		}
		for _, img := range imgs {
			if img.Kind == domain.ImagePoster {
				want = append(want, wantedImage{kind: domain.ImagePoster, source: img.Source, path: img.Path})
			}
		}
	case !store.IsNotFound(err):
		return err
	}
	err = a.store.Write(ctx, func(q store.Q) error {
		if _, err := q.SetMetadata(ctx, item.ID, domain.Metadata{Title: item.Title, SortTitle: item.SortTitle}, a.now()); err != nil {
			return err
		}
		return a.syncImages(ctx, q, item.ID, want)
	})
	if err != nil {
		return err
	}
	a.jobs.Kick()
	a.itemsChanged(item.LibraryID, item.ID)
	return nil
}

// bookCover extracts the cover held in a book file into the metadata folder (once: the name follows
// the file's fingerprint). Without a cover, or if extraction fails (logged), the book has none.
func (a *App) bookCover(ctx context.Context, itemID domain.ID, f domain.MediaFile) (wantedImage, bool) {
	if len(f.Fingerprint) < 8 {
		return wantedImage{}, false
	}
	id := itemID.String()
	dir := filepath.Join(a.metadataDir, "images", id[:2], id)
	prefix := "poster-embedded-" + f.Fingerprint[:8]
	for _, ext := range []string{".jpg", ".png", ".webp", ".gif"} {
		if p := filepath.Join(dir, prefix+ext); fileExists(p) {
			return wantedImage{kind: domain.ImagePoster, source: domain.ImageEmbedded, path: p}, true
		}
	}
	data, ext, err := books.Cover(f.Path)
	if err != nil {
		if !errors.Is(err, books.ErrNoPage) {
			a.log.InfoContext(ctx, "book without a cover", "path", f.Path, "err", err)
		}
		return wantedImage{}, false
	}
	target := filepath.Join(dir, prefix+ext)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		a.log.WarnContext(ctx, "cannot write cover", "path", target, "err", err)
		return wantedImage{}, false
	}
	if err := writeAtomic(target, data); err != nil {
		a.log.WarnContext(ctx, "cannot write cover", "path", target, "err", err)
		return wantedImage{}, false
	}
	return wantedImage{kind: domain.ImagePoster, source: domain.ImageEmbedded, path: target}, true
}

// bookCacheDir holds the resized pages of a file.
func (a *App) bookCacheDir(fileID domain.ID) string {
	id := fileID.String()
	return filepath.Join(a.cacheDir, "books", id[:2], id)
}

// Reading.

// BookReading is what a reader needs to open a book.
type BookReading struct {
	View domain.ItemView
	File domain.BookFile
	// Progress is where the profile is; nil if it has not started the book.
	Progress *domain.ReadingProgress
}

// OpenBook prepares reading a book: the requested file (fileID), otherwise the first present one
// that is read page by page, otherwise the first present one.
func (a *App) OpenBook(ctx context.Context, p domain.Principal, itemID domain.ID, fileID *domain.ID) (BookReading, error) {
	v, err := viewerOf(p)
	if err != nil {
		return BookReading{}, err
	}
	if err := a.checkKind(ctx, itemID, domain.ItemBook); err != nil {
		return BookReading{}, err
	}
	view, err := a.visible(ctx, v, itemID)
	if err != nil {
		return BookReading{}, err
	}
	read := a.store.Read()
	files, err := read.ItemFiles(ctx, itemID)
	if err != nil {
		return BookReading{}, err
	}
	var chosen *domain.BookFile
	for _, f := range files {
		if f.File.MissingSince != nil || (fileID != nil && f.File.ID != *fileID) {
			continue
		}
		bf, err := read.BookFile(ctx, f.File.ID)
		if store.IsNotFound(err) {
			continue // not analyzed yet
		}
		if err != nil {
			return BookReading{}, err
		}
		if chosen == nil || (bf.Layout == domain.LayoutImages && chosen.Layout != domain.LayoutImages) {
			chosen = &bf
		}
	}
	if chosen == nil {
		return BookReading{}, domain.NotFound("book.no_readable_file")
	}
	return BookReading{View: view, File: *chosen, Progress: view.Reading}, nil
}

// SaveReadingProgress stores where the profile is in a book. Past domain.ReadingFinished the book
// is read.
func (a *App) SaveReadingProgress(ctx context.Context, p domain.Principal, itemID domain.ID, rp domain.ReadingProgress) error {
	v, err := viewerOf(p)
	if err != nil {
		return err
	}
	if err := a.checkKind(ctx, itemID, domain.ItemBook); err != nil {
		return err
	}
	view, err := a.visible(ctx, v, itemID)
	if err != nil {
		return err
	}
	switch {
	case rp.Progression < 0 || rp.Progression > 1:
		return domain.Invalid("book.invalid_progress")
	case rp.Page < 0 || rp.Page > 100_000:
		return domain.Invalid("book.invalid_page")
	case len(rp.Locator) > 4096:
		return domain.Invalid("book.location_too_long")
	}
	rp.UpdatedAt = a.now()
	// Read once: the pages after that, read again, do not count as one more read.
	finished := rp.Progression >= domain.ReadingFinished && !view.UserData.Played
	err = a.store.Write(ctx, func(q store.Q) error {
		if err := q.SetReadingProgress(ctx, v.ProfileID, itemID, rp); err != nil {
			return err
		}
		return q.SaveProgress(ctx, v.ProfileID, itemID, 0, finished, rp.UpdatedAt)
	})
	if err == nil {
		a.userDataChanged(v.ProfileID, []domain.ID{itemID})
	}
	return err
}

// BookPageFile is a page ready to serve: a cached file (resized version) or the original image in
// memory.
type BookPageFile struct {
	Path        string
	Data        []byte
	ContentType string
	ETag        string
}

// bookFileFor finds a book file by its ID and key. It is not found if the key does not match, since
// the key stands in for authentication.
func (a *App) bookFileFor(ctx context.Context, fileID domain.ID, key string) (domain.BookFile, domain.MediaFile, error) {
	read := a.store.Read()
	bf, err := read.BookFile(ctx, fileID)
	if store.IsNotFound(err) || (err == nil && subtle.ConstantTimeCompare([]byte(bf.Key), []byte(key)) != 1) {
		return domain.BookFile{}, domain.MediaFile{}, domain.NotFound("book.not_found")
	}
	if err != nil {
		return domain.BookFile{}, domain.MediaFile{}, err
	}
	f, err := read.File(ctx, fileID)
	if store.IsNotFound(err) || (err == nil && f.MissingSince != nil) {
		return domain.BookFile{}, domain.MediaFile{}, domain.NotFound("book.not_found")
	}
	return bf, f, err
}

// BookContent returns the whole file of a book: path, suggested name, content type.
func (a *App) BookContent(ctx context.Context, fileID domain.ID, key string) (filePath, name, contentType string, err error) {
	bf, f, err := a.bookFileFor(ctx, fileID, key)
	if err != nil {
		return "", "", "", err
	}
	return f.Path, strings.ToValidUTF8(filepath.Base(f.Path), "_"), bookTypes[bf.Format], nil
}

// BookPage returns the image of page n of a book read page by page: the original (width = 0, or
// wider than it is), otherwise scaled down to the next image width tier, made on first request and
// cached.
func (a *App) BookPage(ctx context.Context, fileID domain.ID, key string, n, width int) (BookPageFile, error) {
	if width < 0 || width > 10_000 {
		return BookPageFile{}, domain.Invalid("request.invalid_width")
	}
	bf, f, err := a.bookFileFor(ctx, fileID, key)
	if err != nil {
		return BookPageFile{}, err
	}
	if bf.Layout != domain.LayoutImages || n < 0 || n >= len(bf.Pages) {
		return BookPageFile{}, domain.NotFound("book.page_not_found")
	}
	if w, ok := pageTier(bf.Pages[n], width); ok {
		cached := a.pageCachePath(bf, n, w)
		out := BookPageFile{Path: cached, ETag: fmt.Sprintf(`"%s-%d-%d"`, bf.Key, n, w)}
		if !fileExists(cached) {
			if err := a.resizePage(ctx, f.Path, n, w, cached); err != nil {
				return BookPageFile{}, err
			}
		}
		a.warmPages(bf, f.Path, n, width) //nolint:contextcheck // preparation detached from the request, stopped with the server
		return out, nil
	}
	data, ext, err := books.Page(f.Path, n)
	if err != nil {
		return BookPageFile{}, err
	}
	return BookPageFile{Data: data, ContentType: imageTypes[ext], ETag: fmt.Sprintf(`"%s-%d"`, bf.Key, n)}, nil
}

// pagesAhead is how many following pages are prepared ahead, at the same width, while a page is
// being read. Resizing a scanned page takes close to a second, and turning the page should not wait
// for it.
const pagesAhead = 2

// pageTier returns the width tier of a page requested at width; false if the original will do
// (width = 0, or a tier at least as wide as the page).
func pageTier(size domain.PageSize, width int) (int, bool) {
	i, _ := slices.BinarySearch(imageWidths, width)
	if width <= 0 || i == len(imageWidths) || imageWidths[i] >= size.Width {
		return 0, false
	}
	return imageWidths[i], true
}

func (a *App) pageCachePath(bf domain.BookFile, n, w int) string {
	return filepath.Join(a.bookCacheDir(bf.FileID), bf.Key, fmt.Sprintf("%d-%d", n, w))
}

// resizePage writes page n scaled down to w pixels into cached.
func (a *App) resizePage(ctx context.Context, filePath string, n, w int, cached string) error {
	select {
	case a.resizing <- struct{}{}:
		defer func() { <-a.resizing }()
	case <-ctx.Done():
		return ctx.Err()
	}
	data, _, err := books.Page(filePath, n)
	if err != nil {
		return err
	}
	return images.ResizeData(data, fmt.Sprintf("%s (page %d)", filePath, n), cached, w)
}

// warmPages prepares in the background the pages that follow n, at the same width (each one only
// once; stopped with the server).
func (a *App) warmPages(bf domain.BookFile, filePath string, n, width int) {
	ctx := a.runCtx
	if ctx == nil {
		return
	}
	for next := n + 1; next <= n+pagesAhead && next < len(bf.Pages); next++ {
		w, ok := pageTier(bf.Pages[next], width)
		cached := a.pageCachePath(bf, next, w)
		if !ok || fileExists(cached) {
			continue
		}
		if _, busy := a.warming.LoadOrStore(cached, true); busy {
			continue
		}
		a.background.Go(func() {
			defer a.warming.Delete(cached)
			if err := a.resizePage(ctx, filePath, next, w, cached); err != nil && ctx.Err() == nil {
				a.log.WarnContext(ctx, "cannot prepare page ahead", "path", filePath, "page", next, "err", err)
			}
		})
	}
}

// purgeBookCache deletes the cached pages of files that are no longer known books, and those of a
// replaced key.
func (a *App) purgeBookCache(ctx context.Context) error {
	root := filepath.Join(a.cacheDir, "books")
	shards, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	read := a.store.Read()
	for _, shard := range shards {
		files, err := os.ReadDir(filepath.Join(root, shard.Name()))
		if err != nil {
			continue
		}
		for _, fe := range files {
			dir := filepath.Join(root, shard.Name(), fe.Name())
			id, err := domain.ParseID(fe.Name())
			if err != nil {
				continue
			}
			bf, err := read.BookFile(ctx, id)
			if store.IsNotFound(err) {
				_ = os.RemoveAll(dir)
				continue
			}
			if err != nil {
				return err
			}
			keys, _ := os.ReadDir(dir)
			for _, k := range keys {
				if k.Name() != bf.Key {
					_ = os.RemoveAll(filepath.Join(dir, k.Name()))
				}
			}
		}
	}
	return nil
}

// Catalog.

// ListBookSeries returns a page of book series.
func (a *App) ListBookSeries(ctx context.Context, p domain.Principal, lq ListQuery) (ListPage, error) {
	if lq.Played != nil {
		return ListPage{}, domain.Invalid("catalog.played_filter_unsupported")
	}
	return a.list(ctx, p, domain.ItemBookSeries, lq)
}

// ListBooks returns a page of books, from a whole library or from one series.
func (a *App) ListBooks(ctx context.Context, p domain.Principal, lq ListQuery) (ListPage, error) {
	if lq.SeriesID != nil {
		if err := a.checkKind(ctx, *lq.SeriesID, domain.ItemBookSeries); err != nil {
			return ListPage{}, err
		}
	}
	return a.list(ctx, p, domain.ItemBook, lq)
}

// BookSeries returns the details of a book series and its present books, in volume order.
func (a *App) BookSeries(ctx context.Context, p domain.Principal, id domain.ID) (domain.ItemView, []domain.ItemView, error) {
	view, _, err := a.itemWithDetails(ctx, p, id, domain.ItemBookSeries, false)
	if err != nil {
		return domain.ItemView{}, nil, err
	}
	v, err := viewerOf(p)
	if err != nil {
		return domain.ItemView{}, nil, err
	}
	list, err := a.store.Read().SeriesBooks(ctx, v, id)
	return view, list, err
}

// Book returns the details of a book: authors and illustrators, genres, files and how to read them.
func (a *App) Book(ctx context.Context, p domain.Principal, id domain.ID) (domain.ItemView, Details, error) {
	return a.itemWithDetails(ctx, p, id, domain.ItemBook, true)
}
