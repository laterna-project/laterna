package store

import (
	"context"
	"math"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/store/sqlc"
)

// Playback history.

// AddPlay stores a play.
func (q Q) AddPlay(ctx context.Context, p domain.Play) error {
	return q.q.InsertPlay(ctx, sqlc.InsertPlayParams{
		ID: p.ID, ProfileID: p.ProfileID, ItemID: p.ItemID, Kind: string(p.Kind), SeriesID: p.SeriesID, AlbumID: p.AlbumID,
		ArtistID: p.ArtistID, Title: p.Title, Subtitle: p.Subtitle, StartedAt: toMillis(p.StartedAt), EndedAt: toMillis(p.EndedAt),
		WatchedMs: p.Watched.Milliseconds(), PositionMs: p.Position.Milliseconds(), DurationMs: p.Duration.Milliseconds(),
		Completed: toInt(p.Completed), Device: p.Device, Offline: toInt(p.Offline),
	})
}

// PlayCursor is the start time and ID of the last play of a page (zero for the first page).
type PlayCursor struct {
	At time.Time
	ID domain.ID
}

// Plays lists the plays of a profile, newest first, after the cursor.
func (q Q) Plays(ctx context.Context, profileID domain.ID, after PlayCursor, limit int) ([]domain.Play, error) {
	beforeAt, beforeID := int64(math.MaxInt64), domain.ID{}
	for i := range beforeID {
		beforeID[i] = 0xff
	}
	if !after.At.IsZero() {
		beforeAt, beforeID = toMillis(after.At), after.ID
	}
	rows, err := q.q.ListPlays(ctx, sqlc.ListPlaysParams{ProfileID: profileID, BeforeAt: beforeAt, BeforeID: beforeID, Lim: int64(limit)})
	if err != nil {
		return nil, err
	}
	return playsFromRows(rows), nil
}

// playRange converts a period (zero bounds mean no limit) to milliseconds.
func playRange(from, to time.Time) (fromAt, toAt int64) {
	fromAt, toAt = math.MinInt64, math.MaxInt64
	if !from.IsZero() {
		fromAt = toMillis(from)
	}
	if !to.IsZero() {
		toAt = toMillis(to)
	}
	return fromAt, toAt
}

// EachPlay passes to fn, in order, each play of a profile that started between from and to
// (excluded); all of them if both are zero. Plays are streamed and cut down to what statistics use
// (kind, item, series, artist, titles, start, time watched): an extreme year of 20,000 plays is
// walked in 33 ms with 4 MB allocated, against 66 ms and 33 MB for full plays in a slice. fn must
// not read the database, since the connection is held during the walk.
func (q Q) EachPlay(ctx context.Context, profileID domain.ID, from, to time.Time, fn func(*domain.Play)) error {
	fromAt, toAt := playRange(from, to)
	rows, err := q.db.QueryContext(ctx, `SELECT kind, item_id, series_id, artist_id, title, subtitle, started_at, watched_ms
		FROM play_history WHERE profile_id = ? AND started_at >= ? AND started_at < ? ORDER BY started_at, id`, profileID, fromAt, toAt)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			p            domain.Play
			kind         string
			started, wMs int64
		)
		if err := rows.Scan(&kind, &p.ItemID, &p.SeriesID, &p.ArtistID, &p.Title, &p.Subtitle, &started, &wMs); err != nil {
			return err
		}
		p.Kind, p.StartedAt, p.Watched = domain.ItemKind(kind), fromMillis(started), time.Duration(wMs)*time.Millisecond
		fn(&p)
	}
	return rows.Err()
}

// FirstLastPlays returns the first and last plays of a profile that started between from and to
// (excluded), in full; nil if there is none.
func (q Q) FirstLastPlays(ctx context.Context, profileID domain.ID, from, to time.Time) (first, last *domain.Play, err error) {
	fromAt, toAt := playRange(from, to)
	f, err := q.q.FirstPlay(ctx, sqlc.FirstPlayParams{ProfileID: profileID, FromAt: fromAt, ToAt: toAt})
	if IsNotFound(err) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	l, err := q.q.LastPlay(ctx, sqlc.LastPlayParams{ProfileID: profileID, FromAt: fromAt, ToAt: toAt})
	if err != nil {
		return nil, nil, err
	}
	fp, lp := playFromRow(f), playFromRow(l)
	return &fp, &lp, nil
}

// DeletePlay deletes one play of the profile; false if it does not exist (or belongs to another
// profile).
func (q Q) DeletePlay(ctx context.Context, profileID, id domain.ID) (bool, error) {
	n, err := q.q.DeletePlay(ctx, sqlc.DeletePlayParams{ID: id, ProfileID: profileID})
	return n > 0, err
}

// ClearPlays wipes the whole history of a profile and returns the number of plays deleted.
func (q Q) ClearPlays(ctx context.Context, profileID domain.ID) (int64, error) {
	return q.q.DeleteProfilePlays(ctx, profileID)
}

// GenresOf returns the genres of each given item (movies, series).
func (q Q) GenresOf(ctx context.Context, ids []domain.ID) (map[domain.ID][]string, error) {
	out := map[domain.ID][]string{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.q.ListGenresOfItems(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.ItemID] = append(out[r.ItemID], r.Genre)
	}
	return out, nil
}

func playsFromRows(rows []sqlc.PlayHistory) []domain.Play {
	out := make([]domain.Play, len(rows))
	for i, r := range rows {
		out[i] = playFromRow(r)
	}
	return out
}

func playFromRow(r sqlc.PlayHistory) domain.Play {
	return domain.Play{
		ID: r.ID, ProfileID: r.ProfileID, ItemID: r.ItemID, Kind: domain.ItemKind(r.Kind), SeriesID: r.SeriesID,
		AlbumID: r.AlbumID, ArtistID: r.ArtistID, Title: r.Title, Subtitle: r.Subtitle,
		StartedAt: fromMillis(r.StartedAt), EndedAt: fromMillis(r.EndedAt),
		Watched: time.Duration(r.WatchedMs) * time.Millisecond, Position: time.Duration(r.PositionMs) * time.Millisecond,
		Duration: time.Duration(r.DurationMs) * time.Millisecond, Completed: r.Completed == 1, Device: r.Device,
		Offline: r.Offline == 1,
	}
}

// ViewsByID returns, by ID, the cards of the given items that the profile can still see and play
// (history entries, statistics rankings, recommendations).
func (q Q) ViewsByID(ctx context.Context, v domain.Viewer, ids []domain.ID) (map[domain.ID]domain.ItemView, error) {
	views, err := q.presentViews(ctx, v, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[domain.ID]domain.ItemView, len(views))
	for _, view := range views {
		out[view.Item.ID] = view
	}
	return out, nil
}
