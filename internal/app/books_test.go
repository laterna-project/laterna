package app

import (
	"bytes"
	"context"
	"image"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
)

// credited returns the names for a role.
func credited(credits []domain.Credit, role domain.PersonRole) []string {
	var out []string
	for _, c := range credits {
		if c.Role == role {
			out = append(out, c.Name)
		}
	}
	return out
}

// Books on the fixtures: an EPUB novel prepared by Calibre (series, HTML description), a book from
// a Calibre library (metadata.opf and cover.jpg next to it), a CBZ manga with ComicInfo.xml, a
// volume without ComicInfo named release-style, a scanned PDF comic, a text PDF document.
func TestBooks(t *testing.T) {
	a, _ := startMediaApp(t)
	_, p := setupAdmin(t, a)
	ctx := context.Background()
	lib, err := a.CreateLibrary(ctx, "Livres", domain.LibraryBooks, []string{testRoot("Livres")}, "")
	mustNil(t, err)
	waitIdle(t, a)

	series, err := a.ListBookSeries(ctx, p, ListQuery{})
	mustNil(t, err)
	if got := titles(series.Items); !slices.Equal(got, []string{"BD Numerisee", "Manga Test", "Le Voyage"}) {
		t.Fatalf("series: %v", got)
	}
	all, err := a.ListBooks(ctx, p, ListQuery{})
	mustNil(t, err)
	if all.Total != 6 {
		t.Fatalf("books: %v", titles(all.Items))
	}

	// Manga: ComicInfo for the first volume, the name for the second; cover of the series.
	manga, books, err := a.BookSeries(ctx, p, series.Items[1].Item.ID)
	mustNil(t, err)
	if manga.BookCount != 2 || imageSource(manga, domain.ImagePoster) != domain.ImageEmbedded {
		t.Errorf("series: %+v", manga)
	}
	if got := titles(books); len(books) != 2 || !slices.Equal(got, []string{"Le Commencement", domain.BookTitle("Manga Test", 2)}) {
		t.Fatalf("volumes: %v", got)
	}
	if books[1].Book == nil || books[1].Book.Number != 2 || books[1].SeriesTitle != "Manga Test" {
		t.Errorf("second volume: %+v", books[1])
	}
	tome1, d, err := a.Book(ctx, p, books[0].Item.ID)
	mustNil(t, err)
	if tome1.Item.PremiereDate != "2020-05-12" || tome1.Item.Overview != "Un manga de test." ||
		!slices.Equal(credited(d.Credits, domain.RoleWriter), []string{"Scénariste Test"}) ||
		!slices.Equal(credited(d.Credits, domain.RoleIllustrator), []string{"Dessinateur Test"}) ||
		!slices.Equal(d.Genres, []string{"Action", "Aventure"}) || len(d.Files) != 1 || d.Files[0].Book == nil || d.Files[0].Version != "CBZ" {
		t.Errorf("first volume: %+v %+v", tome1, d)
	}

	// Reading page by page: right to left, a double page, pages resized on demand.
	r, err := a.OpenBook(ctx, p, tome1.Item.ID, nil)
	mustNil(t, err)
	want := []domain.PageSize{{Width: 400, Height: 600}, {Width: 400, Height: 600}, {Width: 800, Height: 600}}
	if r.File.Layout != domain.LayoutImages || !r.File.RightToLeft || !slices.Equal(r.File.Pages, want) || r.Progress != nil {
		t.Fatalf("open: %+v", r)
	}
	page, err := a.BookPage(ctx, r.File.FileID, r.File.Key, 2, 300)
	mustNil(t, err)
	data, err := os.ReadFile(page.Path)
	mustNil(t, err)
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err != nil || cfg.Width != 320 || cfg.Height != 240 {
		t.Errorf("resized page: %+v %v", cfg, err)
	}
	// The following pages are prepared ahead, at the same width.
	if _, err := a.BookPage(ctx, r.File.FileID, r.File.Key, 0, 300); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(10 * time.Second); !fileExists(a.pageCachePath(r.File, 1, 320)) || !fileExists(a.pageCachePath(r.File, 2, 320)); {
		if time.Now().After(deadline) {
			t.Fatal("following pages never prepared")
		}
		time.Sleep(20 * time.Millisecond)
	}
	orig, err := a.BookPage(ctx, r.File.FileID, r.File.Key, 0, 0)
	mustNil(t, err)
	if orig.ContentType != "image/png" || len(orig.Data) == 0 || orig.Path != "" {
		t.Errorf("original page: %s %d", orig.ContentType, len(orig.Data))
	}
	if _, err := a.BookPage(ctx, r.File.FileID, "wrong-key", 0, 0); !isKind(err, domain.ErrNotFound) {
		t.Errorf("wrong key: %v", err)
	}
	if _, err := a.BookPage(ctx, r.File.FileID, r.File.Key, 3, 0); !isKind(err, domain.ErrNotFound) {
		t.Errorf("page outside the book: %v", err)
	}

	// Novel prepared by Calibre.
	voyage, novels, err := a.BookSeries(ctx, p, series.Items[2].Item.ID)
	mustNil(t, err)
	if len(novels) != 1 || voyage.BookCount != 1 {
		t.Fatalf("Le Voyage: %+v", novels)
	}
	novel, d, err := a.Book(ctx, p, novels[0].Item.ID)
	mustNil(t, err)
	if novel.Item.Title != "Le Voyage, tome 2 : La Traversée" || novel.Book == nil || novel.Book.Number != 2 ||
		novel.Book.Publisher != "Éditions Test" || novel.Book.ISBN != "9782123456803" || novel.Book.Language != "fr" ||
		novel.Item.PremiereDate != "2023-12-01" || novel.Item.Overview != "Un roman de test.\n\nDeuxième paragraphe." ||
		!slices.Equal(credited(d.Credits, domain.RoleWriter), []string{"Autrice Test"}) || imageSource(novel, domain.ImagePoster) != domain.ImageEmbedded {
		t.Errorf("novel: %+v %+v %+v", novel.Item, novel.Book, d)
	}
	r, err = a.OpenBook(ctx, p, novel.Item.ID, nil)
	mustNil(t, err)
	path, name, contentType, err := a.BookContent(ctx, r.File.FileID, r.File.Key)
	mustNil(t, err)
	if r.File.Layout != domain.LayoutReflowable || contentType != "application/epub+zip" || !strings.HasSuffix(path, ".epub") || name != "Le Voyage T2 - Autrice Test.epub" {
		t.Errorf("EPUB: %+v %s %s", r.File, name, contentType)
	}

	// Calibre library, scanned PDF and PDF document: standalone, except the comic.
	byTitle := map[string]domain.ItemView{}
	for _, v := range all.Items {
		byTitle[v.Item.Title] = v
	}
	calibre, ok := byTitle["Roman Seul"]
	if !ok || calibre.Item.Overview != "Un roman rangé par Calibre." || calibre.Item.ParentID != nil || imageSource(calibre, domain.ImagePoster) != domain.ImageLocal {
		t.Errorf("Calibre: %+v", calibre)
	}
	guide, ok := byTitle["Guide pratique de l'essai"]
	if !ok || guide.Item.PremiereDate != "2022-01-15" || len(guide.Images) != 0 {
		t.Fatalf("document: %+v", guide)
	}
	if r, err := a.OpenBook(ctx, p, guide.Item.ID, nil); err != nil || r.File.Layout != domain.LayoutDocument || r.File.PageCount != 1 {
		t.Errorf("document opened: %+v %v", r.File, err)
	}
	scan, ok := byTitle[domain.BookTitle("BD Numerisee", 1)]
	if !ok || scan.Item.Year != 2019 || imageSource(scan, domain.ImagePoster) != domain.ImageEmbedded {
		t.Fatalf("scanned PDF: %+v", scan)
	}
	if r, err := a.OpenBook(ctx, p, scan.Item.ID, nil); err != nil || r.File.Layout != domain.LayoutImages || len(r.File.Pages) != 3 {
		t.Errorf("scanned PDF opened: %+v %v", r.File, err)
	}

	// Progress: "Continue reading", then read, then read again from the start.
	mustNil(t, a.SaveReadingProgress(ctx, p, tome1.Item.ID, domain.ReadingProgress{Page: 1, Progression: 0.5}))
	rows, err := a.Home(ctx, p, 0)
	mustNil(t, err)
	var reading, latest *HomeRow
	for i := range rows {
		switch rows[i].Kind {
		case RowReading:
			reading = &rows[i]
		case RowLatestBooks:
			latest = &rows[i]
		case RowResume, RowNextUp, RowRecommended, RowBecauseYouWatched, RowRecentAlbums, RowLatestMovies, RowLatestSeries,
			RowLatestAlbums, RowLatestPhotos:
		}
	}
	if reading == nil || len(reading.Items) != 1 || reading.Items[0].Reading == nil || reading.Items[0].Reading.Page != 1 {
		t.Fatalf("continue reading: %+v", reading)
	}
	if latest == nil || len(latest.Items) != 6 || latest.Library == nil || latest.Library.ID != lib.ID {
		t.Errorf("recently added: %+v", latest)
	}
	if err := a.SaveReadingProgress(ctx, p, tome1.Item.ID, domain.ReadingProgress{Progression: 1.5}); !isKind(err, domain.ErrInvalid) {
		t.Errorf("invalid progress: %v", err)
	}
	mustNil(t, a.SaveReadingProgress(ctx, p, tome1.Item.ID, domain.ReadingProgress{Page: 2, Progression: 1}))
	mustNil(t, a.SaveReadingProgress(ctx, p, tome1.Item.ID, domain.ReadingProgress{Page: 2, Progression: 1}))
	done, _, err := a.Book(ctx, p, tome1.Item.ID)
	mustNil(t, err)
	if !done.UserData.Played || done.UserData.PlayCount != 1 {
		t.Errorf("book read once: %+v", done.UserData)
	}
	if rows, _ := a.Home(ctx, p, 0); slices.ContainsFunc(rows, func(r HomeRow) bool { return r.Kind == RowReading }) {
		t.Error("a read book must not be listed to continue")
	}
	mustNil(t, a.SetPlayed(ctx, p, tome1.Item.ID, false))
	if again, _, err := a.Book(ctx, p, tome1.Item.ID); err != nil || again.Reading != nil || again.UserData.Played {
		t.Errorf("read again from the start: %+v %v", again.Reading, err)
	}
	// A series is read once all its books are.
	mustNil(t, a.SetPlayed(ctx, p, manga.Item.ID, true))
	if m, _, err := a.BookSeries(ctx, p, manga.Item.ID); err != nil || !m.UserData.Played {
		t.Errorf("series read: %+v %v", m.UserData, err)
	}

	// Search: series and books.
	found, err := a.Search(ctx, p, "voyage", 10)
	mustNil(t, err)
	if len(found) != 2 || found[0].Item.Kind != domain.ItemBookSeries || found[1].Item.Kind != domain.ItemBook {
		t.Errorf("search: %v", titles(found))
	}
	// A book cannot go in a playlist or a watch party.
	if _, err := a.CreatePlaylist(ctx, p, "Livres", []domain.ID{tome1.Item.ID}); !isKind(err, domain.ErrInvalid) {
		t.Errorf("playlist: %v", err)
	}
	// No rating: a kid profile sees books (access is set per library).
	kid, err := a.CreateProfile(ctx, p, "Enfant", "", true, nil, "")
	mustNil(t, err)
	kp := p
	kp.Profile = &kid
	if kids, err := a.ListBooks(ctx, kp, ListQuery{}); err != nil || kids.Total != 6 {
		t.Errorf("kid profile: %d %v", kids.Total, err)
	}
}
