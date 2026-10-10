package app

import (
	"context"
	"image"
	"math"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
)

// photoImage returns the analyzed image of a photo (kind photo).
func photoImage(t *testing.T, v domain.ItemView) domain.Image {
	t.Helper()
	for _, img := range v.Images {
		if img.Kind == domain.ImagePhoto {
			return img
		}
	}
	t.Fatalf("%s has no image", v.Item.Title)
	return domain.Image{}
}

// Photos on the fixtures: nested albums, a photo rotated by its orientation, dates with and without
// a time zone, a location, a PNG with EXIF, a photo without a date, NAS thumbnails ignored.
func TestPhotos(t *testing.T) {
	a, _ := startMediaApp(t)
	_, p := setupAdmin(t, a)
	ctx := context.Background()
	lib, err := a.CreateLibrary(ctx, "Photos", domain.LibraryPhotos, []string{testRoot("Photos")}, "")
	mustNil(t, err)
	waitIdle(t, a)

	// Timeline: most recently taken first.
	page, err := a.ListPhotos(ctx, p, PhotoQuery{})
	mustNil(t, err)
	if got := titles(page.Items); page.Total != 4 || !slices.Equal(got, []string{"Christmas", "IMG_0002", "IMG_0001", "IMG_9999"}) {
		t.Fatalf("timeline: %v (%d)", got, page.Total)
	}
	byTitle := map[string]domain.ItemView{}
	for _, v := range page.Items {
		if v.Photo == nil {
			t.Fatalf("%s without its details", v.Item.Title)
		}
		byTitle[v.Item.Title] = v
	}
	first := byTitle["IMG_0001"]
	ph := first.Photo
	if !ph.TakenAt.Equal(time.Date(2024, 7, 14, 16, 32, 5, 0, time.UTC)) || ph.UTCOffset == nil || *ph.UTCOffset != 120 ||
		ph.Width != 640 || ph.Height != 480 || ph.Make != "Maker" || ph.Model != "Test Camera" || ph.FNumber != 1.8 ||
		ph.ExposureTime != "1/250" || ph.ISO != 100 || ph.FocalLength != 4.2 || ph.Latitude == nil ||
		math.Abs(*ph.Latitude-48.3904) > 1e-4 || first.Item.PremiereDate != "2024-07-14" {
		t.Errorf("IMG_0001: %+v %+v", ph, first.Item)
	}
	if img := photoImage(t, first); img.Width != 640 || img.Height != 480 || img.BlurHash == "" {
		t.Errorf("image of IMG_0001: %+v", img)
	} else if !fileExists(a.imageCachePath(img.Hash, 240)) || !fileExists(a.imageCachePath(img.Hash, 480)) {
		t.Error("thumbnails not prepared at analysis time")
	}
	// Stored sideways with orientation 6: upright when displayed.
	turned := byTitle["IMG_0002"]
	if turned.Photo.Width != 480 || turned.Photo.Height != 640 || turned.Photo.UTCOffset != nil {
		t.Errorf("IMG_0002: %+v", turned.Photo)
	}
	img := photoImage(t, turned)
	if img.Width != 480 || img.Height != 640 {
		t.Errorf("image of IMG_0002: %+v", img)
	}
	file, err := a.Image(ctx, img.ID, img.Hash, 240)
	mustNil(t, err)
	if f, err := os.Open(file.Path); err == nil {
		cfg, _, err := image.DecodeConfig(f)
		_ = f.Close()
		if err != nil || cfg.Width != 240 || cfg.Height != 320 {
			t.Errorf("rotated resized version: %+v %v", cfg, err)
		}
	}
	// No time zone: the camera's clock in the server's zone. No EXIF: the file date.
	if want := time.Date(2024, 12, 24, 20, 0, 0, 0, time.Local); !byTitle["Christmas"].Photo.TakenAt.Equal(want) {
		t.Errorf("Christmas: %v", byTitle["Christmas"].Photo.TakenAt)
	}
	if undated := byTitle["IMG_9999"]; !undated.Photo.TakenAt.Equal(time.Date(2023, 1, 1, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("no date: %v", undated.Photo.TakenAt)
	}

	// By page, and by month.
	p1, err := a.ListPhotos(ctx, p, PhotoQuery{PageSize: 3})
	mustNil(t, err)
	p2, err := a.ListPhotos(ctx, p, PhotoQuery{PageSize: 3, PageToken: p1.NextPageToken})
	mustNil(t, err)
	if len(p1.Items) != 3 || p1.NextPageToken == "" || len(p2.Items) != 1 || p2.NextPageToken != "" || p2.Items[0].Item.Title != "IMG_9999" {
		t.Errorf("pages: %v / %v", titles(p1.Items), titles(p2.Items))
	}
	months, err := a.PhotoMonths(ctx, p, PhotoQuery{})
	mustNil(t, err)
	if want := []domain.PhotoMonth{{Year: 2024, Month: 12, Count: 1}, {Year: 2024, Month: 7, Count: 2}, {Year: 2023, Month: 1, Count: 1}}; !slices.Equal(months, want) {
		t.Errorf("months: %+v", months)
	}

	// Albums: one per folder, nested; cover from the most recent photo.
	albums, err := a.PhotoAlbums(ctx, p, &lib.ID)
	mustNil(t, err)
	if got := titles(albums); len(albums) != 2 || !slices.Equal(got, []string{"2024", "2023"}) {
		t.Fatalf("albums: %v", got)
	}
	y2024, sub, err := a.PhotoAlbum(ctx, p, albums[0].Item.ID)
	mustNil(t, err)
	if y2024.PhotoCount != 1 || y2024.AlbumCount != 1 || len(sub) != 1 || sub[0].Item.Title != "Holidays" || sub[0].PhotoCount != 2 ||
		imageSource(y2024, domain.ImagePoster) != domain.ImageLocal {
		t.Fatalf("album 2024: %+v %v", y2024, titles(sub))
	}
	inAlbum, err := a.ListPhotos(ctx, p, PhotoQuery{AlbumID: &sub[0].Item.ID})
	mustNil(t, err)
	if got := titles(inAlbum.Items); !slices.Equal(got, []string{"IMG_0002", "IMG_0001"}) {
		t.Errorf("photos of Holidays: %v", got)
	}

	// Favorite yes, "played" no. Search finds albums, not photo names.
	if err := a.SetPlayed(ctx, p, first.Item.ID, true); !isKind(err, domain.ErrInvalid) {
		t.Errorf("photo marked played: %v", err)
	}
	mustNil(t, a.SetFavorite(ctx, p, first.Item.ID, true))
	if fav, err := a.ListPhotos(ctx, p, PhotoQuery{FavoritesOnly: true}); err != nil || len(fav.Items) != 1 {
		t.Errorf("favorites: %v %v", titles(fav.Items), err)
	}
	if found, err := a.Search(ctx, p, "holidays", 10); err != nil || len(found) != 1 || found[0].Item.Kind != domain.ItemPhotoAlbum {
		t.Errorf("search for an album: %v %v", titles(found), err)
	}
	if found, err := a.Search(ctx, p, "IMG", 10); err != nil || len(found) != 0 {
		t.Errorf("search for a photo: %v %v", titles(found), err)
	}
	rows, err := a.Home(ctx, p, 0, false)
	mustNil(t, err)
	if !slices.ContainsFunc(rows, func(r HomeRow) bool { return r.Kind == RowLatestPhotos && len(r.Items) == 4 }) {
		t.Error("recently added photos missing from the home page")
	}
}

// A photo moved to another folder keeps its identity (favorite) and changes album; the emptied
// album goes away.
func TestPhotoMoved(t *testing.T) {
	a, _ := startMediaApp(t)
	_, p := setupAdmin(t, a)
	ctx := context.Background()
	root := t.TempDir()
	copyTree(t, testRoot("Photos"), root)
	lib, err := a.CreateLibrary(ctx, "Photos", domain.LibraryPhotos, []string{root}, "")
	mustNil(t, err)
	waitIdle(t, a)
	page, err := a.ListPhotos(ctx, p, PhotoQuery{})
	mustNil(t, err)
	var moved domain.ItemView
	for _, v := range page.Items {
		if v.Item.Title == "IMG_9999" {
			moved = v
		}
	}
	mustNil(t, a.SetFavorite(ctx, p, moved.Item.ID, true))
	mustNil(t, os.Rename(filepath.Join(root, "2023", "IMG_9999.jpg"), filepath.Join(root, "2024", "Holidays", "IMG_9999.jpg")))
	mustNil(t, a.ScanLibrary(ctx, lib.ID))
	waitIdle(t, a)
	again, _, err := a.Photo(ctx, p, moved.Item.ID)
	mustNil(t, err)
	parent, err := a.store.Read().Item(ctx, *again.Item.ParentID)
	mustNil(t, err)
	if !again.UserData.Favorite || parent.Title != "Holidays" {
		t.Errorf("moved photo: favorite %v, album %q", again.UserData.Favorite, parent.Title)
	}
	albums, err := a.PhotoAlbums(ctx, p, nil)
	mustNil(t, err)
	if got := titles(albums); !slices.Equal(got, []string{"2024"}) {
		t.Errorf("emptied album still there: %v", got)
	}
}
