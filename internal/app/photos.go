package app

import (
	"context"
	"errors"
	"image"
	"os"
	"path"
	"strings"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/jobs"
	"github.com/laterna-project/laterna/internal/media/exif"
	"github.com/laterna-project/laterna/internal/naming"
	"github.com/laterna-project/laterna/internal/store"
)

// Photos: one album per folder (nested like the folders), one photo per file, described by its EXIF
// data. The photo is the image of its item (domain.ImagePhoto): it goes through image analysis
// (dimensions, blurhash, hash) and the image route (cached resized versions).

// reparent gives an item the wanted parent if it has another one: an item found again by its key or
// by its moved file (followMove) keeps its identity but may change series or album.
func (a *App) reparent(ctx context.Context, q store.Q, it domain.Item, parent *domain.ID) error {
	same := (it.ParentID == nil && parent == nil) || (it.ParentID != nil && parent != nil && *it.ParentID == *parent)
	if same {
		return nil
	}
	return q.SetItemParent(ctx, it.ID, parent, a.now())
}

// photoOf reads what a photo says about itself: displayed dimensions, EXIF data, time taken (the
// file date otherwise).
func photoOf(f domain.MediaFile) (domain.Photo, error) {
	file, err := os.Open(f.Path)
	if err != nil {
		return domain.Photo{}, err
	}
	cfg, _, err := image.DecodeConfig(file)
	_ = file.Close()
	if err != nil {
		return domain.Photo{}, err
	}
	info, err := exif.ReadFile(f.Path)
	if err != nil && !errors.Is(err, exif.ErrNone) {
		info = exif.Info{} // damaged EXIF data: the photo can still be shown
	}
	p := domain.Photo{
		Width: cfg.Width, Height: cfg.Height, Make: info.Make, Model: info.Model, Lens: info.Lens, FNumber: info.FNumber,
		ExposureTime: info.ExposureTime, ISO: info.ISO, FocalLength: info.FocalLength, Latitude: info.Latitude, Longitude: info.Longitude,
	}
	if info.Orientation >= 5 {
		p.Width, p.Height = p.Height, p.Width
	}
	taken, offset, ok := info.TakenAt(time.Local)
	if !ok {
		taken = f.ModTime
	}
	p.TakenAt, p.UTCOffset = taken.UTC(), offset
	return p, nil
}

// takenDate is the local date a picture was taken: in camera time if its time zone is known, in
// server time otherwise.
func takenDate(p domain.Photo) time.Time {
	if p.UTCOffset != nil {
		return p.TakenAt.In(time.FixedZone("", *p.UTCOffset*60))
	}
	return p.TakenAt.In(time.Local)
}

// analyzePhoto analyzes a photo and files it in its albums.
func (a *App) analyzePhoto(ctx context.Context, lib domain.Library, f domain.MediaFile, rel string) error {
	p, err := photoOf(f)
	now := a.now()
	if err != nil {
		_ = a.store.Write(ctx, func(q store.Q) error { return q.SetFileAnalysisError(ctx, f.ID, err.Error(), now) })
		return jobs.Permanent(err)
	}
	var albums []domain.ID
	var photoID domain.ID
	var removed int64
	err = a.store.Write(ctx, func(q store.Q) error {
		info := domain.MediaInfo{Container: strings.TrimPrefix(strings.ToLower(path.Ext(rel)), ".")}
		if err := q.SetFileAnalysis(ctx, f.ID, info, now); err != nil {
			return err
		}
		var err error
		if photoID, albums, err = a.placePhoto(ctx, q, lib, f, rel, p); err != nil {
			return err
		}
		// Each album takes its most recent photo as its cover.
		for _, id := range albums {
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
	a.itemsChanged(lib.ID, append(albums, photoID)...)
	if removed > 0 {
		a.libraryChanged(lib.ID)
	}
	return nil
}

// placePhoto files a photo: its albums (one per folder, created if needed), then the photo and its
// image. It returns the photo and its albums, from the top one down to the closest.
func (a *App) placePhoto(ctx context.Context, q store.Q, lib domain.Library, f domain.MediaFile, rel string, p domain.Photo) (domain.ID, []domain.ID, error) {
	var albums []domain.ID
	var parent *domain.ID
	if dir := path.Dir(rel); dir != "." {
		parts := strings.Split(dir, "/")
		for i, name := range parts {
			album, _, err := a.findOrCreate(ctx, q, domain.Item{
				LibraryID: lib.ID, Kind: domain.ItemPhotoAlbum, ParentID: parent, Title: name,
				GroupKey: "album:" + strings.ToLower(strings.Join(parts[:i+1], "/")),
			})
			if err != nil {
				return domain.ID{}, nil, err
			}
			parent = &album.ID
			albums = append(albums, album.ID)
		}
	}
	key := "photo:" + strings.ToLower(rel)
	if err := a.followMove(ctx, q, lib.ID, f.ID, key, false); err != nil {
		return domain.ID{}, nil, err
	}
	title := strings.TrimSuffix(path.Base(rel), path.Ext(rel))
	day := takenDate(p)
	photo, _, err := a.findOrCreate(ctx, q, domain.Item{
		LibraryID: lib.ID, Kind: domain.ItemPhoto, ParentID: parent, GroupKey: key, Title: title, Year: day.Year(),
	})
	if err != nil {
		return domain.ID{}, nil, err
	}
	if err := a.reparent(ctx, q, photo, parent); err != nil {
		return domain.ID{}, nil, err
	}
	p.ItemID = photo.ID
	if err := q.SetPhoto(ctx, p); err != nil {
		return domain.ID{}, nil, err
	}
	// Everything that describes it comes from the file: written here, without a separate job.
	if _, err := q.SetMetadata(ctx, photo.ID, domain.Metadata{
		Title: title, SortTitle: naming.SortTitle(title), Year: day.Year(), PremiereDate: day.Format(time.DateOnly),
	}, a.now()); err != nil {
		return domain.ID{}, nil, err
	}
	if err := q.LinkFile(ctx, photo.ID, f.ID, "", 0); err != nil {
		return domain.ID{}, nil, err
	}
	want := []wantedImage{{kind: domain.ImagePhoto, source: domain.ImageLocal, path: f.Path}}
	return photo.ID, albums, a.syncImages(ctx, q, photo.ID, want)
}

// refreshPhotoAlbum gives an album the cover of its most recent photo (its own or one from an album
// inside it).
func (a *App) refreshPhotoAlbum(ctx context.Context, item domain.Item) error {
	read := a.store.Read()
	var want []wantedImage
	latest, err := read.LatestPhotoInAlbum(ctx, item.ID)
	switch {
	case err == nil:
		imgs, err := read.ItemImages(ctx, latest)
		if err != nil {
			return err
		}
		for _, img := range imgs {
			if img.Kind == domain.ImagePhoto {
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
		if err := a.syncImages(ctx, q, item.ID, want); err != nil {
			return err
		}
		// The album that contains it may change cover too.
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

// Catalog.

// PhotoQuery describes a page of a photo timeline.
type PhotoQuery struct {
	// LibraryID restricts to one library, AlbumID to the photos of one album; nil means everything.
	LibraryID, AlbumID *domain.ID
	FavoritesOnly      bool
	PageSize           int
	PageToken          string
}

// photoQuery checks a timeline query and converts it for the store.
func (a *App) photoQuery(ctx context.Context, p domain.Principal, pq PhotoQuery) (store.PhotoQuery, error) {
	v, err := viewerOf(p)
	if err != nil {
		return store.PhotoQuery{}, err
	}
	size := pq.PageSize
	switch {
	case size == 0:
		size = defaultPageSize
	case size < 0 || size > maxPageSize:
		return store.PhotoQuery{}, domain.Invalid("request.invalid_page_size", "max", maxPageSize)
	}
	sq := store.PhotoQuery{Viewer: v, LibraryID: pq.LibraryID, AlbumID: pq.AlbumID, Favorite: pq.FavoritesOnly, Limit: size}
	if pq.PageToken != "" {
		tok, err := decodePageToken(pq.PageToken)
		if err != nil || tok.Sort != sortTaken {
			return store.PhotoQuery{}, domain.Invalid("request.invalid_page_token")
		}
		sq.After = &tok.Cursor
	}
	if pq.LibraryID != nil && !v.AllowsLibrary(*pq.LibraryID) {
		return store.PhotoQuery{}, domain.NotFound("library.not_found")
	}
	if pq.AlbumID != nil {
		if err := a.checkKind(ctx, *pq.AlbumID, domain.ItemPhotoAlbum); err != nil {
			return store.PhotoQuery{}, err
		}
		if _, err := a.visible(ctx, v, *pq.AlbumID); err != nil {
			return store.PhotoQuery{}, err
		}
	}
	return sq, nil
}

// sortTaken marks the page tokens of a timeline (sorted by time taken).
const sortTaken domain.ItemSort = "taken"

// ListPhotos returns a page of photos, most recently taken first.
func (a *App) ListPhotos(ctx context.Context, p domain.Principal, pq PhotoQuery) (ListPage, error) {
	sq, err := a.photoQuery(ctx, p, pq)
	if err != nil {
		return ListPage{}, err
	}
	read := a.store.Read()
	items, next, err := read.Photos(ctx, sq)
	if err != nil {
		return ListPage{}, err
	}
	total, err := read.CountPhotos(ctx, sq)
	if err != nil {
		return ListPage{}, err
	}
	page := ListPage{Items: items, Total: total}
	if next != nil {
		page.NextPageToken = encodePageToken(pageToken{Sort: sortTaken, Cursor: *next})
	}
	return page, nil
}

// PhotoMonths counts the photos of each month of a timeline, most recent first.
func (a *App) PhotoMonths(ctx context.Context, p domain.Principal, pq PhotoQuery) ([]domain.PhotoMonth, error) {
	sq, err := a.photoQuery(ctx, p, pq)
	if err != nil {
		return nil, err
	}
	return a.store.Read().PhotoMonths(ctx, sq)
}

// PhotoAlbums lists the top-level albums of a library (all libraries if libraryID is nil), starting
// with the one that got photos most recently.
func (a *App) PhotoAlbums(ctx context.Context, p domain.Principal, libraryID *domain.ID) ([]domain.ItemView, error) {
	v, err := viewerOf(p)
	if err != nil {
		return nil, err
	}
	if libraryID != nil && !v.AllowsLibrary(*libraryID) {
		return nil, domain.NotFound("library.not_found")
	}
	return a.store.Read().PhotoAlbums(ctx, v, libraryID, nil)
}

// PhotoAlbum returns an album and the albums inside it. Its photos are read with ListPhotos.
func (a *App) PhotoAlbum(ctx context.Context, p domain.Principal, id domain.ID) (domain.ItemView, []domain.ItemView, error) {
	view, _, err := a.itemWithDetails(ctx, p, id, domain.ItemPhotoAlbum, false)
	if err != nil {
		return domain.ItemView{}, nil, err
	}
	v, err := viewerOf(p)
	if err != nil {
		return domain.ItemView{}, nil, err
	}
	sub, err := a.store.Read().PhotoAlbums(ctx, v, nil, &id)
	return view, sub, err
}

// Photo returns the details of a photo and its file.
func (a *App) Photo(ctx context.Context, p domain.Principal, id domain.ID) (domain.ItemView, Details, error) {
	return a.itemWithDetails(ctx, p, id, domain.ItemPhoto, true)
}
