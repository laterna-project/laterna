package store

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/store/sqlc"
)

// SaveProgress records where a profile is in an item: a resume point (0 means nothing to resume),
// or finished (played, one more play, resume point cleared).
func (q Q) SaveProgress(ctx context.Context, profileID, itemID domain.ID, resume time.Duration, finished bool, now time.Time) error {
	last := sql.NullInt64{Int64: toMillis(now), Valid: true}
	if finished {
		return q.q.SaveFinished(ctx, sqlc.SaveFinishedParams{ProfileID: profileID, ItemID: itemID, LastPlayedAt: last, UpdatedAt: toMillis(now)})
	}
	return q.q.SaveResumePosition(ctx, sqlc.SaveResumePositionParams{
		ProfileID: profileID, ItemID: itemID, PositionMs: resume.Milliseconds(), LastPlayedAt: last, UpdatedAt: toMillis(now),
	})
}

// Resume lists the present movies and episodes a profile has started (a position is stored), most
// recently played first.
func (q Q) Resume(ctx context.Context, v domain.Viewer, limit int) ([]domain.ItemView, error) {
	b := cardQuery(v)
	b.where("i.kind IN ('movie', 'episode')").where("u.position_ms > 0").where(presentCond(domain.ItemMovie)).flushWhere()
	b.add(" ORDER BY u.last_played_at DESC, i.id LIMIT ?", limit)
	return q.cards(ctx, b)
}

// NextUp returns, for each series the profile is watching, the present unplayed episode that
// follows the last one played (specials, season 0, aside), most recently watched series first. A
// finished series is not in the list.
func (q Q) NextUp(ctx context.Context, v domain.Viewer, limit int) ([]domain.ItemView, error) {
	var b query
	b.add(`SELECT n.next_id FROM (
		SELECT l.last_played_at, (
			SELECT e2.item_id FROM episodes e2
			LEFT JOIN user_data u2 ON u2.item_id = e2.item_id AND u2.profile_id = ?
			WHERE e2.series_id = l.series_id AND e2.season_number > 0
				AND (e2.season_number, e2.number) > (l.season_number, l.last_number)
				AND COALESCE(u2.played, 0) = 0 AND `+fmt.Sprintf(presentFileOf, "e2.item_id")+`
			ORDER BY e2.season_number, e2.number LIMIT 1
		) AS next_id
		FROM (
			SELECT e.series_id, e.season_number, MAX(e.number, e.number_end) AS last_number, u.last_played_at,
				ROW_NUMBER() OVER (PARTITION BY e.series_id
					ORDER BY u.last_played_at DESC, e.season_number DESC, e.number DESC) AS rn
			FROM user_data u JOIN episodes e ON e.item_id = u.item_id
			WHERE u.profile_id = ? AND u.played = 1 AND u.last_played_at IS NOT NULL
		) l
		WHERE l.rn = 1
	) n
	WHERE n.next_id IS NOT NULL
	ORDER BY n.last_played_at DESC LIMIT ?`, v.ProfileID, v.ProfileID, limit)
	rows, err := q.db.QueryContext(ctx, b.String(), b.args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var ids []domain.ID
	for rows.Next() {
		var id domain.ID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return q.views(ctx, v, ids)
}

// LatestSeries lists the present series of a library, starting with the one that got an episode
// most recently. Present episodes are read newest first through the date-added index, in batches,
// until there are enough distinct series: a batch of new episodes usually touches few series.
func (q Q) LatestSeries(ctx context.Context, v domain.Viewer, libraryID domain.ID, limit int) ([]domain.ItemView, error) {
	var out []domain.ItemView
	seen := map[domain.ID]bool{}
	after := latestCursor{at: math.MaxInt64}
	for len(out) < limit {
		ids, more, err := q.latestSeriesBatch(ctx, libraryID, &after, seen)
		if err != nil {
			return nil, err
		}
		views, err := q.views(ctx, v, ids) // those the profile can see
		if err != nil {
			return nil, err
		}
		out = append(out, views...)
		if !more {
			break
		}
	}
	return out[:min(limit, len(out))], nil
}

// latestBatch is how many episodes LatestSeries reads per batch (a variable so tests can change
// it).
var latestBatch = 500

// latestCursor is the date added and ID of the last episode read.
type latestCursor struct {
	at int64
	id domain.ID
}

// latestSeriesBatch reads the next batch of episodes and returns the series not seen yet. Rows are
// read and closed before the visibility filter, which needs another connection: holding one while
// waiting for the other would lock up the whole pool under load.
func (q Q) latestSeriesBatch(ctx context.Context, libraryID domain.ID, after *latestCursor, seen map[domain.ID]bool) ([]domain.ID, bool, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT le.series_id, ei.added_at, ei.id FROM items ei JOIN episodes le ON le.item_id = ei.id
		WHERE ei.library_id = ? AND ei.kind = 'episode' AND ei.present = 1 AND (ei.added_at, ei.id) < (?, ?)
		ORDER BY ei.added_at DESC, ei.id DESC LIMIT ?`, libraryID, after.at, after.id, latestBatch)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = rows.Close() }()
	var ids []domain.ID
	n := 0
	for rows.Next() {
		var series domain.ID
		if err := rows.Scan(&series, &after.at, &after.id); err != nil {
			return nil, false, err
		}
		n++
		if !seen[series] {
			seen[series] = true
			ids = append(ids, series)
		}
	}
	return ids, n == latestBatch, rows.Err()
}

// RecentAlbums lists the present albums the profile has played a track of, most recent listen
// first.
func (q Q) RecentAlbums(ctx context.Context, v domain.Viewer, limit int) ([]domain.ItemView, error) {
	b := cardQuery(v)
	b.add(` JOIN (SELECT rt.album_id, MAX(ru.last_played_at) AS played_at FROM tracks rt
		JOIN user_data ru ON ru.item_id = rt.item_id AND ru.profile_id = ?
		WHERE ru.last_played_at IS NOT NULL GROUP BY rt.album_id) rp ON rp.album_id = i.id`, v.ProfileID)
	b.where("i.kind = 'album'").where(presentCond(domain.ItemAlbum)).flushWhere()
	b.add(" ORDER BY rp.played_at DESC, i.id LIMIT ?", limit)
	return q.cards(ctx, b)
}

// views reads the views of several items, in the order of the given IDs (minus those the profile
// may not see).
func (q Q) views(ctx context.Context, v domain.Viewer, ids []domain.ID) ([]domain.ItemView, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	b := cardQuery(v)
	b.where("i.id IN ("+placeholders(len(ids))+")", args...).flushWhere()
	found, err := q.cards(ctx, b)
	if err != nil {
		return nil, err
	}
	byID := make(map[domain.ID]domain.ItemView, len(found))
	for _, v := range found {
		byID[v.Item.ID] = v
	}
	out := make([]domain.ItemView, 0, len(ids))
	for _, id := range ids {
		if v, ok := byID[id]; ok {
			out = append(out, v)
		}
	}
	return out, nil
}
