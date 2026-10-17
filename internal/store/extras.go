package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/store/sqlc"
)

// Extras of movies and series: items whose parent is their movie or series.

// SetExtra stores the kind of an extra.
func (q Q) SetExtra(ctx context.Context, x domain.Extra) error {
	return q.q.UpsertExtra(ctx, sqlc.UpsertExtraParams{ItemID: x.ItemID, Kind: string(x.Kind)})
}

// Extras lists the present extras of a movie or a series that a profile sees: trailers first, then
// by kind and title.
func (q Q) Extras(ctx context.Context, v domain.Viewer, owner domain.ID) ([]domain.ItemView, error) {
	b := cardQuery(v)
	b.add(" JOIN extras x ON x.item_id = i.id")
	b.where("i.kind = 'extra'").where("i.parent_id = ?", owner).where(presentHere)
	b.flushWhere()
	b.add(` ORDER BY CASE x.kind WHEN 'trailer' THEN 0 WHEN 'featurette' THEN 1 WHEN 'behind_the_scenes' THEN 2
		WHEN 'deleted_scene' THEN 3 WHEN 'interview' THEN 4 WHEN 'scene' THEN 5 WHEN 'short' THEN 6 ELSE 7 END,
		i.sort_title, i.id`)
	return q.cards(ctx, b)
}

// ExtraSummary counts the present extras of a movie or a series and gives its first trailer, nil if
// it has none.
func (q Q) ExtraSummary(ctx context.Context, owner domain.ID) (int, *domain.ID, error) {
	n, err := q.q.CountExtras(ctx, &owner)
	if err != nil || n == 0 {
		return int(n), nil, err
	}
	trailer, err := q.q.FirstTrailer(ctx, &owner)
	if errors.Is(err, sql.ErrNoRows) {
		return int(n), nil, nil
	}
	if err != nil {
		return 0, nil, err
	}
	return int(n), &trailer, nil
}

// attachExtras attaches their kind to the extras among the cards, in one query.
func (q Q) attachExtras(ctx context.Context, cards []domain.ItemView) error {
	if len(cards) == 0 {
		return nil
	}
	var ids []any
	index := map[domain.ID]int{}
	for i, c := range cards {
		if c.Item.Kind == domain.ItemExtra {
			ids = append(ids, c.Item.ID)
			index[c.Item.ID] = i
		}
	}
	if len(ids) == 0 {
		return nil
	}
	var b query
	b.add("SELECT item_id, kind FROM extras WHERE item_id IN ("+placeholders(len(ids))+")", ids...)
	rows, err := q.db.QueryContext(ctx, b.String(), b.args...)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var x domain.Extra
		var kind string
		if err := rows.Scan(&x.ItemID, &kind); err != nil {
			return err
		}
		x.Kind = domain.ExtraKind(kind)
		if i, ok := index[x.ItemID]; ok {
			cards[i].Extra = &x
		}
	}
	return rows.Err()
}

// MoviesInFolder returns the movies with a present file directly in a folder (absolute path), and
// those files.
func (q Q) MoviesInFolder(ctx context.Context, libraryID domain.ID, dir string) (map[domain.ID][]string, error) {
	from, to := under(dir)
	rows, err := q.q.ListMovieFilesUnder(ctx, sqlc.ListMovieFilesUnderParams{LibraryID: libraryID, FromPath: from, ToPath: to})
	if err != nil {
		return nil, err
	}
	out := map[domain.ID][]string{}
	for _, r := range rows {
		if filepath.Dir(r.Path) == filepath.Clean(dir) {
			out[r.ItemID] = append(out[r.ItemID], r.Path)
		}
	}
	return out, nil
}

// FileUnder is a present file below a folder, and the kind of its item ("" if it has none).
type FileUnder struct {
	ID       domain.ID
	Path     string
	ItemKind domain.ItemKind
}

// FilesUnder returns the present files below a folder (absolute path), with the kind of their item.
func (q Q) FilesUnder(ctx context.Context, libraryID domain.ID, dir string) ([]FileUnder, error) {
	from, to := under(dir)
	rows, err := q.q.ListFilesUnder(ctx, sqlc.ListFilesUnderParams{LibraryID: libraryID, FromPath: from, ToPath: to})
	if err != nil {
		return nil, err
	}
	out := make([]FileUnder, len(rows))
	for i, r := range rows {
		out[i] = FileUnder{ID: r.ID, Path: r.Path, ItemKind: domain.ItemKind(r.ItemKind)}
	}
	return out, nil
}

// under gives the range of the paths below a folder: from the folder and its separator, up to the
// next character after the separator.
func under(dir string) (string, string) {
	prefix := filepath.Clean(dir) + string(os.PathSeparator)
	return prefix, prefix[:len(prefix)-1] + string(rune(os.PathSeparator+1))
}
