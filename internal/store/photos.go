package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/store/sqlc"
)

// Photos.

// SetPhoto stores the photo-specific part of an item.
func (q Q) SetPhoto(ctx context.Context, p domain.Photo) error {
	offset := sql.NullInt64{}
	if p.UTCOffset != nil {
		offset = sql.NullInt64{Int64: int64(*p.UTCOffset), Valid: true}
	}
	return q.q.UpsertPhoto(ctx, sqlc.UpsertPhotoParams{
		ItemID: p.ItemID, TakenAt: toMillis(p.TakenAt), UtcOffset: offset, Width: int64(p.Width), Height: int64(p.Height),
		Make: p.Make, Model: p.Model, Lens: p.Lens, FNumber: p.FNumber, ExposureTime: p.ExposureTime, Iso: int64(p.ISO),
		FocalLength: p.FocalLength, Latitude: nullFloat(p.Latitude), Longitude: nullFloat(p.Longitude),
	})
}

// Photo reads the photo-specific part of an item.
func (q Q) Photo(ctx context.Context, itemID domain.ID) (domain.Photo, error) {
	r, err := q.q.GetPhoto(ctx, itemID)
	if err != nil {
		return domain.Photo{}, err
	}
	return photoFromRow(r), nil
}

func photoFromRow(r sqlc.Photo) domain.Photo {
	p := domain.Photo{
		ItemID: r.ItemID, TakenAt: fromMillis(r.TakenAt), Width: int(r.Width), Height: int(r.Height), Make: r.Make,
		Model: r.Model, Lens: r.Lens, FNumber: r.FNumber, ExposureTime: r.ExposureTime, ISO: int(r.Iso),
		FocalLength: r.FocalLength, Latitude: optFloat(r.Latitude), Longitude: optFloat(r.Longitude),
	}
	if r.UtcOffset.Valid {
		o := int(r.UtcOffset.Int64)
		p.UTCOffset = &o
	}
	return p
}

// PhotoQuery describes a page of a photo timeline.
type PhotoQuery struct {
	Viewer domain.Viewer
	// LibraryID restricts to one library, AlbumID to the photos of one album (not of the albums
	// inside it); nil means everything.
	LibraryID, AlbumID *domain.ID
	Favorite           bool
	// After is the last photo of the previous page (Num is the time taken, in ms).
	After *Cursor
	Limit int
}

func photoFilters(b *query, pq PhotoQuery) {
	b.where("i.kind = 'photo'").where(presentCond(domain.ItemPhoto))
	if pq.LibraryID != nil {
		b.where("i.library_id = ?", *pq.LibraryID)
	}
	if pq.AlbumID != nil {
		b.where("i.parent_id = ?", *pq.AlbumID)
	}
	if pq.Favorite {
		b.where("u.favorite = 1")
	}
}

// Photos returns a page of photos, most recently taken first, and the cursor of the next page (nil
// at the end).
func (q Q) Photos(ctx context.Context, pq PhotoQuery) ([]domain.ItemView, *Cursor, error) {
	b := cardQuery(pq.Viewer)
	b.add(" JOIN photos ph ON ph.item_id = i.id")
	photoFilters(b, pq)
	if pq.After != nil {
		b.where("(ph.taken_at, i.id) < (?, ?)", int64(pq.After.Num), pq.After.ID)
	}
	b.flushWhere()
	b.add(" ORDER BY ph.taken_at DESC, i.id DESC LIMIT ?", pq.Limit+1)
	cards, err := q.cards(ctx, b)
	if err != nil || len(cards) <= pq.Limit {
		return cards, nil, err
	}
	cards = cards[:pq.Limit]
	last := cards[len(cards)-1]
	next := &Cursor{ID: last.Item.ID}
	if last.Photo != nil {
		next.Num = float64(last.Photo.TakenAt.UnixMilli())
	}
	return cards, next, nil
}

// CountPhotos counts the photos of a timeline, all pages together.
func (q Q) CountPhotos(ctx context.Context, pq PhotoQuery) (int, error) {
	var b query
	b.add("SELECT COUNT(*) FROM items i LEFT JOIN user_data u ON u.item_id = i.id AND u.profile_id = ?", pq.Viewer.ProfileID)
	photoFilters(&b, pq)
	viewerFilter(&b, pq.Viewer)
	b.flushWhere()
	var n int
	err := q.db.QueryRowContext(ctx, b.String(), b.args...).Scan(&n)
	return n, err
}

// takenMonth is the month the picture was taken, in camera time if its time zone is known, in
// server time otherwise.
const takenMonth = `CASE WHEN ph.utc_offset IS NOT NULL
	THEN strftime('%Y-%m', ph.taken_at / 1000 + ph.utc_offset * 60, 'unixepoch')
	ELSE strftime('%Y-%m', ph.taken_at / 1000, 'unixepoch', 'localtime') END`

// PhotoMonths counts the photos of each month of a timeline, most recent first.
func (q Q) PhotoMonths(ctx context.Context, pq PhotoQuery) ([]domain.PhotoMonth, error) {
	var b query
	b.add("SELECT "+takenMonth+" AS month, COUNT(*) FROM items i LEFT JOIN user_data u ON u.item_id = i.id AND u.profile_id = ?"+
		" JOIN photos ph ON ph.item_id = i.id", pq.Viewer.ProfileID)
	photoFilters(&b, pq)
	viewerFilter(&b, pq.Viewer)
	b.flushWhere()
	b.add(" GROUP BY month ORDER BY month DESC")
	rows, err := q.db.QueryContext(ctx, b.String(), b.args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []domain.PhotoMonth
	for rows.Next() {
		var month string
		var m domain.PhotoMonth
		if err := rows.Scan(&month, &m.Count); err != nil {
			return nil, err
		}
		if _, err := fmt.Sscanf(month, "%d-%d", &m.Year, &m.Month); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// PhotoAlbums lists the albums of a library (the top-level ones if parentID is nil, otherwise those
// inside an album), starting with the one that got photos most recently, then by title.
func (q Q) PhotoAlbums(ctx context.Context, v domain.Viewer, libraryID, parentID *domain.ID) ([]domain.ItemView, error) {
	b := cardQuery(v)
	b.where("i.kind = 'photo_album'").where(presentCond(domain.ItemPhotoAlbum))
	if libraryID != nil {
		b.where("i.library_id = ?", *libraryID)
	}
	if parentID != nil {
		b.where("i.parent_id = ?", *parentID)
	} else {
		b.where("i.parent_id IS NULL")
	}
	b.flushWhere()
	b.add(" ORDER BY (SELECT MAX(lp.taken_at) FROM photos lp JOIN items li ON li.id = lp.item_id WHERE li.parent_id = i.id) DESC, i.sort_title")
	return q.cards(ctx, b)
}

// LatestPhotoInAlbum returns the most recent present photo of an album and of the albums inside it
// (its cover).
func (q Q) LatestPhotoInAlbum(ctx context.Context, albumID domain.ID) (domain.ID, error) {
	var id domain.ID
	err := q.db.QueryRowContext(ctx, `WITH RECURSIVE tree(id, depth) AS (
			SELECT ?, 0
			UNION ALL SELECT i.id, tree.depth + 1 FROM items i JOIN tree ON i.parent_id = tree.id
			WHERE i.kind = 'photo_album' AND tree.depth < ?)
		SELECT p.item_id FROM photos p JOIN items i ON i.id = p.item_id
		WHERE i.parent_id IN (SELECT id FROM tree) AND `+fmt.Sprintf(presentFileOf, "p.item_id")+`
		ORDER BY p.taken_at DESC LIMIT 1`, albumID, maxAlbumDepth).Scan(&id)
	if err == sql.ErrNoRows {
		return id, ErrNotFound
	}
	return id, err
}

// attachPhotos fills in photo cards (time taken, camera, location) and album cards (photos present,
// albums inside).
func (q Q) attachPhotos(ctx context.Context, cards []domain.ItemView) error {
	if len(cards) == 0 {
		return nil
	}
	var photos, albums []any
	index := map[domain.ID]int{}
	for i, c := range cards {
		switch c.Item.Kind {
		case domain.ItemPhoto:
			photos = append(photos, c.Item.ID)
		case domain.ItemPhotoAlbum:
			albums = append(albums, c.Item.ID)
		case domain.ItemMovie, domain.ItemSeries, domain.ItemSeason, domain.ItemEpisode, domain.ItemArtist,
			domain.ItemAlbum, domain.ItemTrack, domain.ItemBookSeries, domain.ItemBook:
			continue
		}
		index[c.Item.ID] = i
	}
	if len(photos) > 0 {
		var b query
		b.add(`SELECT item_id, taken_at, utc_offset, width, height, make, model, lens, f_number, exposure_time, iso,
			focal_length, latitude, longitude FROM photos WHERE item_id IN (`+placeholders(len(photos))+`)`, photos...)
		rows, err := q.db.QueryContext(ctx, b.String(), b.args...)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var r sqlc.Photo
			if err := rows.Scan(&r.ItemID, &r.TakenAt, &r.UtcOffset, &r.Width, &r.Height, &r.Make, &r.Model, &r.Lens,
				&r.FNumber, &r.ExposureTime, &r.Iso, &r.FocalLength, &r.Latitude, &r.Longitude); err != nil {
				return err
			}
			p := photoFromRow(r)
			cards[index[r.ItemID]].Photo = &p
		}
		if err := rows.Err(); err != nil {
			return err
		}
	}
	if len(albums) > 0 {
		var b query
		b.add(`SELECT a.parent_id,
			SUM(a.kind = 'photo' AND `+fmt.Sprintf(presentFileOf, "a.id")+`), SUM(a.kind = 'photo_album')
			FROM items a WHERE a.parent_id IN (`+placeholders(len(albums))+`) GROUP BY a.parent_id`, albums...)
		rows, err := q.db.QueryContext(ctx, b.String(), b.args...)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var id domain.ID
			var photos, subAlbums int
			if err := rows.Scan(&id, &photos, &subAlbums); err != nil {
				return err
			}
			c := &cards[index[id]]
			c.PhotoCount, c.AlbumCount = photos, subAlbums
		}
		return rows.Err()
	}
	return nil
}
