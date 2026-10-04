package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/store/sqlc"
)

// CreateLibrary stores a library and its folders. ErrDuplicate if the name or a folder is already
// taken.
func (q Q) CreateLibrary(ctx context.Context, l domain.Library) error {
	if err := translate(q.q.InsertLibrary(ctx, sqlc.InsertLibraryParams{
		ID: l.ID, Name: l.Name, NameKey: NameKey(l.Name), Kind: string(l.Kind), Language: l.Language,
		CreatedAt: toMillis(l.CreatedAt), UpdatedAt: toMillis(l.UpdatedAt),
	})); err != nil {
		return err
	}
	return q.setLibraryPaths(ctx, l.ID, l.Paths)
}

// UpdateLibrary rewrites the name, language and folders of a library.
func (q Q) UpdateLibrary(ctx context.Context, l domain.Library) error {
	if err := translate(q.q.UpdateLibrary(ctx, sqlc.UpdateLibraryParams{
		Name: l.Name, NameKey: NameKey(l.Name), Language: l.Language, UpdatedAt: toMillis(l.UpdatedAt), ID: l.ID,
	})); err != nil {
		return err
	}
	if err := q.q.DeleteLibraryPaths(ctx, l.ID); err != nil {
		return err
	}
	return q.setLibraryPaths(ctx, l.ID, l.Paths)
}

func (q Q) setLibraryPaths(ctx context.Context, id domain.ID, paths []string) error {
	for _, p := range paths {
		if err := translate(q.q.InsertLibraryPath(ctx, sqlc.InsertLibraryPathParams{LibraryID: id, Path: p})); err != nil {
			return err
		}
	}
	return nil
}

// Library reads a library.
func (q Q) Library(ctx context.Context, id domain.ID) (domain.Library, error) {
	row, err := q.q.GetLibrary(ctx, id)
	if err != nil {
		return domain.Library{}, err
	}
	paths, err := q.q.ListLibraryPaths(ctx, id)
	if err != nil {
		return domain.Library{}, err
	}
	return libraryFromRow(row, paths), nil
}

// Libraries lists the libraries in their order: those the administrator ranked, at their rank; then
// the others, by kind (movies, series, music, books, photos) and by name.
func (q Q) Libraries(ctx context.Context) ([]domain.Library, error) {
	rows, err := q.q.ListLibraries(ctx)
	if err != nil {
		return nil, err
	}
	all, err := q.q.ListAllLibraryPaths(ctx)
	if err != nil {
		return nil, err
	}
	paths := map[domain.ID][]string{}
	for _, p := range all {
		paths[p.LibraryID] = append(paths[p.LibraryID], p.Path)
	}
	out := make([]domain.Library, len(rows))
	for i, r := range rows {
		out[i] = libraryFromRow(r, paths[r.ID])
	}
	return out, nil
}

// AllLibraryPaths returns every library folder with its library.
func (q Q) AllLibraryPaths(ctx context.Context) (map[string]domain.ID, error) {
	rows, err := q.q.ListAllLibraryPaths(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]domain.ID, len(rows))
	for _, r := range rows {
		out[r.Path] = r.LibraryID
	}
	return out, nil
}

// DeleteLibrary deletes a library and its whole catalog (files, items, images...).
func (q Q) DeleteLibrary(ctx context.Context, id domain.ID) error { return q.q.DeleteLibrary(ctx, id) }

// SetLibraryScanned records the end of a scan.
func (q Q) SetLibraryScanned(ctx context.Context, id domain.ID, at time.Time) error {
	return q.q.SetLibraryScanned(ctx, sqlc.SetLibraryScannedParams{LastScanAt: sql.NullInt64{Int64: toMillis(at), Valid: true}, ID: id})
}

// LibraryItemCounts counts the items of a library by kind.
func (q Q) LibraryItemCounts(ctx context.Context, id domain.ID) (map[domain.ItemKind]int, error) {
	rows, err := q.q.CountLibraryItems(ctx, id)
	if err != nil {
		return nil, err
	}
	out := map[domain.ItemKind]int{}
	for _, r := range rows {
		out[domain.ItemKind(r.Kind)] = int(r.N)
	}
	return out, nil
}

// SetLibraryOrder ranks the libraries in the given order: the first gets rank 1.
func (q Q) SetLibraryOrder(ctx context.Context, ids []domain.ID) error {
	for i, id := range ids {
		if err := q.q.SetLibraryPosition(ctx, sqlc.SetLibraryPositionParams{Position: int64(i + 1), ID: id}); err != nil {
			return err
		}
	}
	return nil
}

func libraryFromRow(r sqlc.Library, paths []string) domain.Library {
	l := domain.Library{
		ID: r.ID, Name: r.Name, Kind: domain.LibraryKind(r.Kind), Paths: paths, Language: r.Language, Position: int(r.Position),
		CreatedAt: fromMillis(r.CreatedAt), UpdatedAt: fromMillis(r.UpdatedAt), LastScanAt: optTime(r.LastScanAt),
	}
	if l.Paths == nil {
		l.Paths = []string{}
	}
	return l
}

func optTime(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := fromMillis(v.Int64)
	return &t
}

func nullMillis(t *time.Time) sql.NullInt64 {
	if t == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: toMillis(*t), Valid: true}
}

// MetadataDirs returns the signature of the metadata files of each known folder of a library (path
// to signature).
func (q Q) MetadataDirs(ctx context.Context, libraryID domain.ID) (map[string]string, error) {
	rows, err := q.q.ListMetadataDirs(ctx, libraryID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		out[r.Path] = r.Signature
	}
	return out, nil
}

// SetMetadataDir stores the signature of the metadata files of a folder; empty means the folder has
// none left.
func (q Q) SetMetadataDir(ctx context.Context, libraryID domain.ID, path, signature string) error {
	if signature == "" {
		return q.q.DeleteMetadataDir(ctx, sqlc.DeleteMetadataDirParams{LibraryID: libraryID, Path: path})
	}
	return q.q.UpsertMetadataDir(ctx, sqlc.UpsertMetadataDirParams{LibraryID: libraryID, Path: path, Signature: signature})
}
