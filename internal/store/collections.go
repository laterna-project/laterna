package store

import (
	"context"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/store/sqlc"
)

// Collections.

// releaseOrder sorts movies and series by release date, then by title.
const releaseOrder = "CASE WHEN i.premiere_date <> '' THEN i.premiere_date ELSE printf('%04d', i.year) END, i.sort_title, i.id"

// maxCollectionImages is how many posters are shown per collection or playlist.
const maxCollectionImages = 4

// NFOSet is a collection named by an NFO, with its key ("tmdb:<id>" or "name:<name>").
type NFOSet struct {
	Key, Name, Overview string
}

// SetNFOCollection puts an item in the collection its NFO names (nil for none) and removes it from
// the other NFO-based collections. NFO-based collections that end up empty are deleted. Manual
// collections are left alone.
func (q Q) SetNFOCollection(ctx context.Context, itemID domain.ID, set *NFOSet, now time.Time) error {
	keep := ""
	if set != nil {
		keep = set.Key
	}
	if err := q.q.DeleteItemFromOtherNFOCollections(ctx, sqlc.DeleteItemFromOtherNFOCollectionsParams{ItemID: itemID, KeepKey: keep}); err != nil {
		return err
	}
	if set != nil {
		c, err := q.q.GetCollectionByNFOKey(ctx, set.Key)
		switch {
		case IsNotFound(err):
			c = sqlc.Collection{ID: domain.NewID(), Name: set.Name, Overview: set.Overview, NfoKey: set.Key}
			if err := q.q.InsertCollection(ctx, sqlc.InsertCollectionParams{
				ID: c.ID, Name: c.Name, Overview: c.Overview, NfoKey: c.NfoKey, CreatedAt: toMillis(now), UpdatedAt: toMillis(now),
			}); err != nil {
				return err
			}
		case err != nil:
			return err
		case c.Name != set.Name || c.Overview != set.Overview:
			if err := q.q.UpdateCollection(ctx, sqlc.UpdateCollectionParams{Name: set.Name, Overview: set.Overview, UpdatedAt: toMillis(now), ID: c.ID}); err != nil {
				return err
			}
		}
		if err := q.q.InsertCollectionItem(ctx, sqlc.InsertCollectionItemParams{CollectionID: c.ID, ItemID: itemID, AddedAt: toMillis(now)}); err != nil {
			return err
		}
	}
	_, err := q.q.DeleteEmptyNFOCollections(ctx)
	return err
}

// CreateCollection stores a manual collection.
func (q Q) CreateCollection(ctx context.Context, c domain.Collection) error {
	return q.q.InsertCollection(ctx, sqlc.InsertCollectionParams{
		ID: c.ID, Name: c.Name, Overview: c.Overview, CreatedAt: toMillis(c.CreatedAt), UpdatedAt: toMillis(c.UpdatedAt),
	})
}

// Collection reads a collection.
func (q Q) Collection(ctx context.Context, id domain.ID) (domain.Collection, error) {
	row, err := q.q.GetCollection(ctx, id)
	return collectionFromRow(row), err
}

// UpdateCollection renames a collection or changes its overview.
func (q Q) UpdateCollection(ctx context.Context, c domain.Collection) error {
	return q.q.UpdateCollection(ctx, sqlc.UpdateCollectionParams{Name: c.Name, Overview: c.Overview, UpdatedAt: toMillis(c.UpdatedAt), ID: c.ID})
}

// DeleteCollection deletes a collection (never its items).
func (q Q) DeleteCollection(ctx context.Context, id domain.ID) error {
	return q.q.DeleteCollection(ctx, id)
}

// AddCollectionItem adds an item to a collection (no effect if it is already there).
func (q Q) AddCollectionItem(ctx context.Context, collectionID, itemID domain.ID, now time.Time) error {
	return q.q.InsertCollectionItem(ctx, sqlc.InsertCollectionItemParams{CollectionID: collectionID, ItemID: itemID, AddedAt: toMillis(now)})
}

// RemoveCollectionItem removes an item from a collection.
func (q Q) RemoveCollectionItem(ctx context.Context, collectionID, itemID domain.ID) error {
	return q.q.DeleteCollectionItem(ctx, sqlc.DeleteCollectionItemParams{CollectionID: collectionID, ItemID: itemID})
}

// ItemCollections lists the collections of an item, by name.
func (q Q) ItemCollections(ctx context.Context, itemID domain.ID) ([]domain.Collection, error) {
	rows, err := q.q.ListItemCollections(ctx, itemID)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Collection, len(rows))
	for i, r := range rows {
		out[i] = collectionFromRow(r)
	}
	return out, nil
}

// Collections lists the collections with at least one item the profile can see, in a library (all
// of them if libraryID is nil), by name. withEmpty adds the manual collections that have nothing
// visible yet, so they can be managed.
func (q Q) Collections(ctx context.Context, v domain.Viewer, libraryID *domain.ID, withEmpty bool) ([]domain.CollectionView, error) {
	var b query
	visible, vargs := viewerAnd(v)
	var lib string
	if libraryID != nil {
		lib = " AND i.library_id = ?"
		vargs = append(vargs, *libraryID)
	}
	b.add(`SELECT id, name, overview, nfo_key, created_at, updated_at, n FROM (
		SELECT c.id, c.name, c.overview, c.nfo_key, c.created_at, c.updated_at,
			(SELECT COUNT(*) FROM collection_items ci JOIN items i ON i.id = ci.item_id
				WHERE ci.collection_id = c.id AND i.kind IN ('movie', 'series') AND `+presentAny+visible+lib+`) AS n
		FROM collections c
	) WHERE n > 0 OR (? AND nfo_key = '')
	ORDER BY name COLLATE NOCASE, id`, append(vargs, toInt(withEmpty))...)
	out, err := q.collectionRows(ctx, &b)
	if err != nil {
		return nil, err
	}
	for i := range out {
		first, err := q.CollectionItems(ctx, v, out[i].ID, maxCollectionImages)
		if err != nil {
			return nil, err
		}
		if out[i].Images, err = q.covers(ctx, v, first); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// collectionRows reads collections and their number of visible items.
func (q Q) collectionRows(ctx context.Context, b *query) ([]domain.CollectionView, error) {
	rows, err := q.db.QueryContext(ctx, b.String(), b.args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []domain.CollectionView
	for rows.Next() {
		var r sqlc.Collection
		var n int64
		if err := rows.Scan(&r.ID, &r.Name, &r.Overview, &r.NfoKey, &r.CreatedAt, &r.UpdatedAt, &n); err != nil {
			return nil, err
		}
		out = append(out, domain.CollectionView{Collection: collectionFromRow(r), ItemCount: int(n)})
	}
	return out, rows.Err()
}

// CollectionItems lists the movies and series of a collection that the profile can see, by release
// date; limit 0 means all.
func (q Q) CollectionItems(ctx context.Context, v domain.Viewer, collectionID domain.ID, limit int) ([]domain.ItemView, error) {
	b := cardQuery(v)
	b.add(" JOIN collection_items ci ON ci.item_id = i.id")
	b.where("ci.collection_id = ?", collectionID).where("i.kind IN ('movie', 'series')").where(presentAny).flushWhere()
	b.add(" ORDER BY " + releaseOrder)
	if limit > 0 {
		b.add(" LIMIT ?", limit)
	}
	return q.cards(ctx, b)
}

// covers returns one image per item that has one: its poster, or failing that its thumb, and for an
// episode without an image the poster of its series; for a track, the cover of its album.
func (q Q) covers(ctx context.Context, v domain.Viewer, views []domain.ItemView) ([]domain.Image, error) {
	var out []domain.Image
	parents := map[domain.ID]*domain.Image{}
	for _, view := range views {
		img := cover(view)
		var parent *domain.ID
		switch {
		case view.Episode != nil:
			parent = &view.Episode.SeriesID
		case view.Track != nil:
			parent = &view.Track.AlbumID
		}
		if img == nil && parent != nil {
			if _, done := parents[*parent]; !done {
				p, err := q.View(ctx, v, *parent)
				if err != nil && !IsNotFound(err) {
					return nil, err
				}
				parents[*parent] = cover(p)
			}
			img = parents[*parent]
		}
		if img != nil {
			out = append(out, *img)
		}
	}
	return out, nil
}

// cover returns the poster of an item, or failing that its thumb (nil without an image).
func cover(view domain.ItemView) *domain.Image {
	var thumb *domain.Image
	for i, img := range view.Images {
		if img.Kind == domain.ImagePoster {
			return &view.Images[i]
		}
		if img.Kind == domain.ImageThumb {
			thumb = &view.Images[i]
		}
	}
	return thumb
}

func collectionFromRow(r sqlc.Collection) domain.Collection {
	return domain.Collection{
		ID: r.ID, Name: r.Name, Overview: r.Overview, Manual: r.NfoKey == "",
		CreatedAt: fromMillis(r.CreatedAt), UpdatedAt: fromMillis(r.UpdatedAt),
	}
}

// Playlists.

// CreatePlaylist stores a playlist.
func (q Q) CreatePlaylist(ctx context.Context, p domain.Playlist) error {
	return q.q.InsertPlaylist(ctx, sqlc.InsertPlaylistParams{
		ID: p.ID, ProfileID: p.ProfileID, Name: p.Name, CreatedAt: toMillis(p.CreatedAt), UpdatedAt: toMillis(p.UpdatedAt),
	})
}

// Playlist reads a playlist.
func (q Q) Playlist(ctx context.Context, id domain.ID) (domain.Playlist, error) {
	row, err := q.q.GetPlaylist(ctx, id)
	return playlistFromRow(row), err
}

// CountPlaylists returns the number of playlists of a profile.
func (q Q) CountPlaylists(ctx context.Context, profileID domain.ID) (int, error) {
	n, err := q.q.CountProfilePlaylists(ctx, profileID)
	return int(n), err
}

// RenamePlaylist renames a playlist.
func (q Q) RenamePlaylist(ctx context.Context, id domain.ID, name string, now time.Time) error {
	return q.q.RenamePlaylist(ctx, sqlc.RenamePlaylistParams{Name: name, UpdatedAt: toMillis(now), ID: id})
}

// TouchPlaylist records that a playlist was changed.
func (q Q) TouchPlaylist(ctx context.Context, id domain.ID, now time.Time) error {
	return q.q.TouchPlaylist(ctx, sqlc.TouchPlaylistParams{UpdatedAt: toMillis(now), ID: id})
}

// DeletePlaylist deletes a playlist.
func (q Q) DeletePlaylist(ctx context.Context, id domain.ID) error {
	return q.q.DeletePlaylist(ctx, id)
}

// PlaylistEntryRef is a playlist entry without the view of its item.
type PlaylistEntryRef struct {
	ID, ItemID domain.ID
}

// PlaylistEntryRefs lists the entries of a playlist, in order.
func (q Q) PlaylistEntryRefs(ctx context.Context, playlistID domain.ID) ([]PlaylistEntryRef, error) {
	rows, err := q.q.ListPlaylistEntries(ctx, playlistID)
	if err != nil {
		return nil, err
	}
	out := make([]PlaylistEntryRef, len(rows))
	for i, r := range rows {
		out[i] = PlaylistEntryRef{ID: r.ID, ItemID: r.ItemID}
	}
	return out, nil
}

// SetPlaylistEntries rewrites the order of a playlist: the given entries in that order (new ones
// are created) and nothing else.
func (q Q) SetPlaylistEntries(ctx context.Context, playlistID domain.ID, entries []PlaylistEntryRef, now time.Time) error {
	before, err := q.PlaylistEntryRefs(ctx, playlistID)
	if err != nil {
		return err
	}
	kept := map[domain.ID]bool{}
	for _, e := range entries {
		kept[e.ID] = true
	}
	known := map[domain.ID]bool{}
	for _, e := range before {
		known[e.ID] = true
		if !kept[e.ID] {
			if err := q.q.DeletePlaylistEntry(ctx, sqlc.DeletePlaylistEntryParams{ID: e.ID, PlaylistID: playlistID}); err != nil {
				return err
			}
		}
	}
	for pos, e := range entries {
		if known[e.ID] {
			err = q.q.SetPlaylistEntryPosition(ctx, sqlc.SetPlaylistEntryPositionParams{Position: int64(pos), ID: e.ID})
		} else {
			err = q.q.InsertPlaylistEntry(ctx, sqlc.InsertPlaylistEntryParams{
				ID: e.ID, PlaylistID: playlistID, ItemID: e.ItemID, Position: int64(pos), AddedAt: toMillis(now),
			})
		}
		if err != nil {
			return err
		}
	}
	return q.TouchPlaylist(ctx, playlistID, now)
}

// PlaylistEntries reads the entries of a playlist that the profile can see (file present), in
// order; limit 0 means all.
func (q Q) PlaylistEntries(ctx context.Context, v domain.Viewer, playlistID domain.ID, limit int) ([]domain.PlaylistEntry, error) {
	refs, err := q.PlaylistEntryRefs(ctx, playlistID)
	if err != nil {
		return nil, err
	}
	ids := make([]domain.ID, 0, len(refs))
	seen := map[domain.ID]bool{}
	for _, r := range refs {
		if !seen[r.ItemID] {
			seen[r.ItemID] = true
			ids = append(ids, r.ItemID)
		}
	}
	views, err := q.presentViews(ctx, v, ids)
	if err != nil {
		return nil, err
	}
	byID := make(map[domain.ID]domain.ItemView, len(views))
	for _, view := range views {
		byID[view.Item.ID] = view
	}
	var out []domain.PlaylistEntry
	for _, r := range refs {
		if view, ok := byID[r.ItemID]; ok {
			out = append(out, domain.PlaylistEntry{ID: r.ID, Item: view})
			if limit > 0 && len(out) == limit {
				break
			}
		}
	}
	return out, nil
}

// presentViews reads the views of the items that have a file present and that the profile can see.
func (q Q) presentViews(ctx context.Context, v domain.Viewer, ids []domain.ID) ([]domain.ItemView, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	b := cardQuery(v)
	b.where("i.id IN ("+placeholders(len(ids))+")", args...).where(presentAny).flushWhere()
	return q.cards(ctx, b)
}

// Playlists lists the playlists of a profile, most recently changed first, with what the profile
// can see of them.
func (q Q) Playlists(ctx context.Context, v domain.Viewer) ([]domain.PlaylistView, error) {
	rows, err := q.q.ListProfilePlaylists(ctx, v.ProfileID)
	if err != nil {
		return nil, err
	}
	out := make([]domain.PlaylistView, len(rows))
	for i, r := range rows {
		if out[i], err = q.PlaylistView(ctx, v, playlistFromRow(r)); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// PlaylistView fills in a playlist: number of visible entries, duration, posters.
func (q Q) PlaylistView(ctx context.Context, v domain.Viewer, p domain.Playlist) (domain.PlaylistView, error) {
	view := domain.PlaylistView{Playlist: p}
	visible, args := viewerAnd(v)
	var b query
	b.add(`SELECT COUNT(*), CAST(COALESCE(SUM(i.runtime_ms), 0) AS INTEGER) FROM playlist_entries pe JOIN items i ON i.id = pe.item_id
		WHERE pe.playlist_id = ? AND `+presentAny+visible, append([]any{p.ID}, args...)...)
	var n, ms int64
	if err := q.db.QueryRowContext(ctx, b.String(), b.args...).Scan(&n, &ms); err != nil {
		return view, err
	}
	view.EntryCount, view.Duration = int(n), time.Duration(ms)*time.Millisecond
	first, err := q.PlaylistEntries(ctx, v, p.ID, maxCollectionImages)
	if err != nil {
		return view, err
	}
	items := make([]domain.ItemView, len(first))
	for i, e := range first {
		items[i] = e.Item
	}
	view.Images, err = q.covers(ctx, v, items)
	return view, err
}

func playlistFromRow(r sqlc.Playlist) domain.Playlist {
	return domain.Playlist{ID: r.ID, ProfileID: r.ProfileID, Name: r.Name, CreatedAt: fromMillis(r.CreatedAt), UpdatedAt: fromMillis(r.UpdatedAt)}
}
