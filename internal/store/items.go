package store

import (
	"context"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/store/sqlc"
)

// Item reads an item.
func (q Q) Item(ctx context.Context, id domain.ID) (domain.Item, error) {
	row, err := q.q.GetItem(ctx, id)
	if err != nil {
		return domain.Item{}, err
	}
	return itemFromRow(row), nil
}

// ItemByGroupKey finds the item of a library that groups a given key.
func (q Q) ItemByGroupKey(ctx context.Context, libraryID domain.ID, key string) (domain.Item, error) {
	row, err := q.q.GetItemByGroupKey(ctx, sqlc.GetItemByGroupKeyParams{LibraryID: libraryID, GroupKey: key})
	if err != nil {
		return domain.Item{}, err
	}
	return itemFromRow(row), nil
}

// CreateItem stores an item (minimal metadata: title, year).
func (q Q) CreateItem(ctx context.Context, it domain.Item) error {
	return translate(q.q.InsertItem(ctx, sqlc.InsertItemParams{
		ID: it.ID, LibraryID: it.LibraryID, Kind: string(it.Kind), ParentID: it.ParentID, GroupKey: it.GroupKey,
		Title: it.Title, SortTitle: it.SortTitle, Year: int64(it.Year),
		AddedAt: toMillis(it.AddedAt), UpdatedAt: toMillis(it.UpdatedAt),
	}))
}

// SetItemGroupKey updates the group key (renamed folder).
func (q Q) SetItemGroupKey(ctx context.Context, id domain.ID, key string, now time.Time) error {
	return translate(q.q.SetItemGroupKey(ctx, sqlc.SetItemGroupKeyParams{GroupKey: key, UpdatedAt: toMillis(now), ID: id}))
}

// SetItemParent changes the parent of an item (a book moved to another series, a photo to another
// album).
func (q Q) SetItemParent(ctx context.Context, id domain.ID, parent *domain.ID, now time.Time) error {
	return q.q.SetItemParent(ctx, sqlc.SetItemParentParams{ParentID: parent, UpdatedAt: toMillis(now), ID: id})
}

// CreateSeason stores the season-specific data.
func (q Q) CreateSeason(ctx context.Context, s domain.Season) error {
	return translate(q.q.InsertSeason(ctx, sqlc.InsertSeasonParams{ItemID: s.ItemID, SeriesID: s.SeriesID, Number: int64(s.Number)}))
}

// CreateEpisode stores the episode-specific data.
func (q Q) CreateEpisode(ctx context.Context, e domain.Episode) error {
	return translate(q.q.InsertEpisode(ctx, sqlc.InsertEpisodeParams{
		ItemID: e.ItemID, SeriesID: e.SeriesID, SeasonID: e.SeasonID, SeasonNumber: int64(e.SeasonNumber),
		Number: int64(e.Number), NumberEnd: int64(e.NumberEnd), Absolute: toInt(e.Absolute),
	}))
}

// Season reads the season-specific data.
func (q Q) Season(ctx context.Context, itemID domain.ID) (domain.Season, error) {
	r, err := q.q.GetSeason(ctx, itemID)
	return domain.Season{ItemID: r.ItemID, SeriesID: r.SeriesID, Number: int(r.Number)}, err
}

// Episode reads the episode-specific data.
func (q Q) Episode(ctx context.Context, itemID domain.ID) (domain.Episode, error) {
	r, err := q.q.GetEpisode(ctx, itemID)
	return domain.Episode{
		ItemID: r.ItemID, SeriesID: r.SeriesID, SeasonID: r.SeasonID, SeasonNumber: int(r.SeasonNumber),
		Number: int(r.Number), NumberEnd: int(r.NumberEnd), Absolute: r.Absolute == 1,
	}, err
}

// LinkFile attaches a file to an item, detaching it from its previous item if it had one.
func (q Q) LinkFile(ctx context.Context, itemID, fileID domain.ID, version string, part int) error {
	return q.q.LinkFile(ctx, sqlc.LinkFileParams{ItemID: itemID, FileID: fileID, Version: version, Part: int64(part)})
}

// FileItem returns the item of a file.
func (q Q) FileItem(ctx context.Context, fileID domain.ID) (domain.ID, error) {
	return q.q.GetFileItem(ctx, fileID)
}

// ItemFile is a file of an item, with its version and part.
type ItemFile struct {
	File    domain.MediaFile
	Version string
	Part    int
}

// ItemFiles lists the files of an item, analysis included.
func (q Q) ItemFiles(ctx context.Context, itemID domain.ID) ([]ItemFile, error) {
	rows, err := q.q.ListItemFiles(ctx, itemID)
	if err != nil {
		return nil, err
	}
	out := make([]ItemFile, 0, len(rows))
	for _, r := range rows {
		f, err := q.fileWithInfo(ctx, fileRow{
			id: r.ID, libraryID: r.LibraryID, path: r.Path, size: r.Size, mtime: r.Mtime, fingerprint: r.Fingerprint,
			missing: r.MissingSince, analyzed: r.AnalyzedAt, container: r.Container, durationMs: r.DurationMs, bitrate: r.Bitrate,
			tags: r.Tags,
		})
		if err != nil {
			return nil, err
		}
		out = append(out, ItemFile{File: f, Version: r.Version, Part: int(r.Part)})
	}
	return out, nil
}

// DeleteOrphanItems deletes the items that became empty: movies, episodes, tracks, books and photos
// without a file, then seasons without episodes, series without seasons, albums without tracks,
// artists without albums, book series without books and empty photo albums (deepest first). It
// returns the number of items deleted.
func (q Q) DeleteOrphanItems(ctx context.Context, libraryID domain.ID) (int64, error) {
	var total int64
	for _, del := range []func(context.Context, domain.ID) (int64, error){
		q.q.DeleteOrphanLeafItems, q.q.DeleteEmptySeasons, q.q.DeleteEmptySeries, q.q.DeleteEmptyAlbums,
		q.q.DeleteEmptyArtists, q.q.DeleteEmptyBookSeries,
	} {
		n, err := del(ctx, libraryID)
		if err != nil {
			return total, err
		}
		total += n
	}
	// Emptying an album may leave the album that contains it empty.
	for range maxAlbumDepth {
		n, err := q.q.DeleteEmptyPhotoAlbums(ctx, libraryID)
		if err != nil || n == 0 {
			return total + n, err
		}
		total += n
	}
	return total, nil
}

// maxAlbumDepth caps the nesting of photo albums (folders).
const maxAlbumDepth = 64

// SetMetadata rewrites the metadata of an item in one go: fields, genres, studios, provider IDs and
// credits. It returns the person of each credit, in the order of m.Credits.
func (q Q) SetMetadata(ctx context.Context, itemID domain.ID, m domain.Metadata, now time.Time) ([]domain.ID, error) {
	if err := q.q.UpdateItemMetadata(ctx, sqlc.UpdateItemMetadataParams{
		Title: m.Title, SortTitle: m.SortTitle, OriginalTitle: m.OriginalTitle, Year: int64(m.Year),
		PremiereDate: m.PremiereDate, Overview: m.Overview, Tagline: m.Tagline, OfficialRating: m.OfficialRating,
		AgeRating:       nullAge(m.AgeRating),
		CommunityRating: m.CommunityRating, RuntimeMs: m.Runtime.Milliseconds(),
		MetadataAt: nullMillis(&now), UpdatedAt: toMillis(now), ID: itemID,
	}); err != nil {
		return nil, err
	}
	if err := q.q.DeleteItemGenres(ctx, itemID); err != nil {
		return nil, err
	}
	for _, g := range m.Genres {
		if err := q.q.InsertItemGenre(ctx, sqlc.InsertItemGenreParams{ItemID: itemID, Genre: g}); err != nil {
			return nil, err
		}
	}
	if err := q.q.DeleteItemStudios(ctx, itemID); err != nil {
		return nil, err
	}
	for _, s := range m.Studios {
		if err := q.q.InsertItemStudio(ctx, sqlc.InsertItemStudioParams{ItemID: itemID, Studio: s}); err != nil {
			return nil, err
		}
	}
	if err := q.q.DeleteProviderIDs(ctx, itemID); err != nil {
		return nil, err
	}
	for provider, value := range m.ProviderIDs {
		if err := q.q.InsertProviderID(ctx, sqlc.InsertProviderIDParams{ItemID: itemID, Provider: provider, Value: value}); err != nil {
			return nil, err
		}
	}
	if err := q.q.DeleteItemPeople(ctx, itemID); err != nil {
		return nil, err
	}
	people := make([]domain.ID, len(m.Credits))
	for i, c := range m.Credits {
		personID, err := q.person(ctx, c.Name, now)
		if err != nil {
			return nil, err
		}
		people[i] = personID
		if err := q.q.InsertItemPerson(ctx, sqlc.InsertItemPersonParams{
			ItemID: itemID, PersonID: personID, Role: string(c.Role), Character: c.Character, SortOrder: int64(c.Order),
		}); err != nil {
			return nil, err
		}
	}
	return people, nil
}

// person finds a person by name, or creates them.
func (q Q) person(ctx context.Context, name string, now time.Time) (domain.ID, error) {
	p, err := q.q.GetPersonByKey(ctx, NameKey(name))
	if err == nil {
		return p.ID, nil
	}
	if !IsNotFound(err) {
		return domain.ID{}, err
	}
	id := domain.NewID()
	return id, q.q.InsertPerson(ctx, sqlc.InsertPersonParams{ID: id, Name: name, NameKey: NameKey(name), CreatedAt: toMillis(now)})
}

// ItemDetails groups what completes an item: genres, studios, IDs, credits.
type ItemDetails struct {
	Genres      []string
	Studios     []string
	ProviderIDs map[string]string
	Credits     []domain.Credit
}

// Details reads the genres, studios, IDs and credits of an item.
func (q Q) Details(ctx context.Context, itemID domain.ID) (ItemDetails, error) {
	var d ItemDetails
	var err error
	if d.Genres, err = q.q.ListItemGenres(ctx, itemID); err != nil {
		return d, err
	}
	if d.Studios, err = q.q.ListItemStudios(ctx, itemID); err != nil {
		return d, err
	}
	ids, err := q.q.ListProviderIDs(ctx, itemID)
	if err != nil {
		return d, err
	}
	d.ProviderIDs = make(map[string]string, len(ids))
	for _, r := range ids {
		d.ProviderIDs[r.Provider] = r.Value
	}
	people, err := q.q.ListItemPeople(ctx, itemID)
	if err != nil {
		return d, err
	}
	for _, p := range people {
		c := domain.Credit{
			PersonID: p.ID, Name: p.Name, Role: domain.PersonRole(p.Role), Character: p.Character, Order: int(p.SortOrder),
		}
		if p.ImageID != nil {
			c.Image = &domain.Image{
				ID: *p.ImageID, Kind: domain.ImagePoster, Width: int(p.ImageWidth), Height: int(p.ImageHeight),
				BlurHash: p.ImageBlurhash, Hash: p.ImageHash,
			}
		}
		d.Credits = append(d.Credits, c)
	}
	return d, nil
}

// Images.

// ItemImages lists the images of an item.
func (q Q) ItemImages(ctx context.Context, itemID domain.ID) ([]domain.Image, error) {
	rows, err := q.q.ListItemImages(ctx, &itemID)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Image, len(rows))
	for i, r := range rows {
		out[i] = imageFromRow(r)
	}
	return out, nil
}

// SetItemImage attaches an image to an item, or replaces the file of the image of the same kind. It
// returns the image, and true if it is new or changed (and needs analyzing).
func (q Q) SetItemImage(ctx context.Context, itemID domain.ID, kind domain.ImageKind, source domain.ImageSource, path, remoteURL string, now time.Time) (domain.Image, bool, error) {
	row, err := q.q.GetItemImage(ctx, sqlc.GetItemImageParams{ItemID: &itemID, Kind: string(kind)})
	switch {
	case err == nil:
		img := imageFromRow(row)
		if img.Path == path && img.Source == source {
			return img, img.Hash == "", nil
		}
		if err := q.q.ReplaceImageFile(ctx, sqlc.ReplaceImageFileParams{
			Source: string(source), Path: path, RemoteUrl: remoteURL, UpdatedAt: toMillis(now), ID: img.ID,
		}); err != nil {
			return domain.Image{}, false, err
		}
		img.Source, img.Path, img.RemoteURL, img.Width, img.Height, img.BlurHash, img.Hash = source, path, remoteURL, 0, 0, "", ""
		return img, true, nil
	case IsNotFound(err):
		img := domain.Image{ID: domain.NewID(), Kind: kind, Source: source, Path: path, RemoteURL: remoteURL, UpdatedAt: now}
		return img, true, q.q.InsertImage(ctx, sqlc.InsertImageParams{
			ID: img.ID, ItemID: &itemID, Kind: string(kind), Source: string(source), Path: path, RemoteUrl: remoteURL,
			UpdatedAt: toMillis(now),
		})
	default:
		return domain.Image{}, false, err
	}
}

// PersonImage reads the photo of a person.
func (q Q) PersonImage(ctx context.Context, personID domain.ID) (domain.Image, error) {
	row, err := q.q.GetPersonImage(ctx, sqlc.GetPersonImageParams{PersonID: &personID, Kind: string(domain.ImagePoster)})
	if err != nil {
		return domain.Image{}, err
	}
	return imageFromRow(row), nil
}

// SetPersonImage gives a person a downloaded photo, or replaces theirs. It returns the image, and
// true if it is new or changed (and needs downloading).
func (q Q) SetPersonImage(ctx context.Context, personID domain.ID, path, remoteURL string, now time.Time) (domain.Image, bool, error) {
	img, err := q.PersonImage(ctx, personID)
	switch {
	case err == nil:
		if img.Path == path {
			return img, img.Hash == "", nil
		}
		if err := q.q.ReplaceImageFile(ctx, sqlc.ReplaceImageFileParams{
			Source: string(domain.ImageRemote), Path: path, RemoteUrl: remoteURL, UpdatedAt: toMillis(now), ID: img.ID,
		}); err != nil {
			return domain.Image{}, false, err
		}
		img.Source, img.Path, img.RemoteURL, img.Width, img.Height, img.BlurHash, img.Hash = domain.ImageRemote, path, remoteURL, 0, 0, "", ""
		return img, true, nil
	case IsNotFound(err):
		img := domain.Image{
			ID: domain.NewID(), PersonID: &personID, Kind: domain.ImagePoster, Source: domain.ImageRemote, Path: path, RemoteURL: remoteURL, UpdatedAt: now,
		}
		return img, true, q.q.InsertImage(ctx, sqlc.InsertImageParams{
			ID: img.ID, PersonID: &personID, Kind: string(img.Kind), Source: string(img.Source), Path: path, RemoteUrl: remoteURL,
			UpdatedAt: toMillis(now),
		})
	default:
		return domain.Image{}, false, err
	}
}

// DeleteOrphanPeople forgets the people who are no longer credited on any item (and their photo).
func (q Q) DeleteOrphanPeople(ctx context.Context) (int64, error) { return q.q.DeleteOrphanPeople(ctx) }

// RemoteImagePaths lists the files of the images kept in the metadata folder (downloaded, or
// extracted covers).
func (q Q) RemoteImagePaths(ctx context.Context) ([]string, error) {
	return q.q.ListRemoteImagePaths(ctx)
}

// DeleteImage removes an image.
func (q Q) DeleteImage(ctx context.Context, id domain.ID) error { return q.q.DeleteImage(ctx, id) }

// Image reads an image.
func (q Q) Image(ctx context.Context, id domain.ID) (domain.Image, error) {
	row, err := q.q.GetImage(ctx, id)
	if err != nil {
		return domain.Image{}, err
	}
	return imageFromRow(row), nil
}

// SetImageAnalysis stores the dimensions, blurhash and content hash of an image.
func (q Q) SetImageAnalysis(ctx context.Context, id domain.ID, width, height int, blurhash, hash string, now time.Time) error {
	return q.q.SetImageAnalysis(ctx, sqlc.SetImageAnalysisParams{
		Width: int64(width), Height: int64(height), Blurhash: blurhash, Hash: hash, UpdatedAt: toMillis(now), ID: id,
	})
}

func imageFromRow(r sqlc.Image) domain.Image {
	return domain.Image{
		ID: r.ID, ItemID: r.ItemID, PersonID: r.PersonID, ThemeID: r.ThemeID, Kind: domain.ImageKind(r.Kind), Source: domain.ImageSource(r.Source), Path: r.Path, RemoteURL: r.RemoteUrl,
		Width: int(r.Width), Height: int(r.Height), BlurHash: r.Blurhash, Hash: r.Hash, UpdatedAt: fromMillis(r.UpdatedAt),
	}
}

func itemFromRow(r sqlc.Item) domain.Item {
	return domain.Item{
		ID: r.ID, LibraryID: r.LibraryID, Kind: domain.ItemKind(r.Kind), ParentID: r.ParentID, GroupKey: r.GroupKey,
		Title: r.Title, SortTitle: r.SortTitle, OriginalTitle: r.OriginalTitle, Year: int(r.Year),
		PremiereDate: r.PremiereDate, Overview: r.Overview, Tagline: r.Tagline, OfficialRating: r.OfficialRating,
		CommunityRating: r.CommunityRating, Runtime: time.Duration(r.RuntimeMs) * time.Millisecond,
		MetadataAt: optTime(r.MetadataAt), AddedAt: fromMillis(r.AddedAt), UpdatedAt: fromMillis(r.UpdatedAt),
	}
}

// FirstFileOfSeries returns the path of a present file of the series (the first episode).
func (q Q) FirstFileOfSeries(ctx context.Context, seriesID domain.ID) (string, error) {
	return q.q.FirstFileOfSeries(ctx, seriesID)
}

// FirstFileOfSeason returns the path of a present file of the season.
func (q Q) FirstFileOfSeason(ctx context.Context, seasonID domain.ID) (string, error) {
	return q.q.FirstFileOfSeason(ctx, seasonID)
}

// CountPresentItems counts the present items of each kind (metrics).
func (q Q) CountPresentItems(ctx context.Context) (map[domain.ItemKind]int, error) {
	rows, err := q.q.CountPresentItemsByKind(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[domain.ItemKind]int, len(rows))
	for _, r := range rows {
		out[domain.ItemKind(r.Kind)] = int(r.N)
	}
	return out, nil
}
