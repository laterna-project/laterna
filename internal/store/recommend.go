package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
)

// Recommendations: what describes each movie and series, and what a profile did with them.

// actorsPerItem is how many actors are kept per item, in billing order.
const actorsPerItem = 5

// Features returns the features of every movie and series.
func (q Q) Features(ctx context.Context) ([]domain.ItemFeatures, error) {
	var out []domain.ItemFeatures
	pos := map[domain.ID]int{}
	if err := q.each(ctx, `SELECT id, kind, year, community_rating FROM items WHERE kind IN ('movie', 'series')`, nil,
		func(rows *sql.Rows) error {
			var f domain.ItemFeatures
			var kind string
			if err := rows.Scan(&f.ID, &kind, &f.Year, &f.Rating); err != nil {
				return err
			}
			f.Kind = domain.ItemKind(kind)
			pos[f.ID] = len(out)
			out = append(out, f)
			return nil
		}); err != nil {
		return nil, err
	}
	owned := ` JOIN items i ON i.id = x.item_id WHERE i.kind IN ('movie', 'series')`
	var (
		id   domain.ID
		text string
	)
	for _, part := range []struct {
		query string
		apply func(f *domain.ItemFeatures)
	}{
		{`SELECT x.item_id, x.genre FROM item_genres x` + owned, func(f *domain.ItemFeatures) { f.Genres = append(f.Genres, text) }},
		{`SELECT x.item_id, x.studio FROM item_studios x` + owned, func(f *domain.ItemFeatures) { f.Studios = append(f.Studios, text) }},
	} {
		if err := q.each(ctx, part.query, nil, func(rows *sql.Rows) error {
			if err := rows.Scan(&id, &text); err != nil {
				return err
			}
			if i, ok := pos[id]; ok {
				part.apply(&out[i])
			}
			return nil
		}); err != nil {
			return nil, err
		}
	}
	var person domain.ID
	if err := q.each(ctx, `SELECT x.item_id, x.person_id, x.role FROM item_people x`+owned+` AND (x.role <> 'actor' OR x.sort_order < ?)`,
		[]any{actorsPerItem}, func(rows *sql.Rows) error {
			if err := rows.Scan(&id, &person, &text); err != nil {
				return err
			}
			if i, ok := pos[id]; ok {
				out[i].People = append(out[i].People, domain.FeaturePerson{ID: person, Role: domain.PersonRole(text)})
			}
			return nil
		}); err != nil {
		return nil, err
	}
	var collection domain.ID
	if err := q.each(ctx, `SELECT x.item_id, x.collection_id FROM collection_items x`+owned, nil, func(rows *sql.Rows) error {
		if err := rows.Scan(&id, &collection); err != nil {
			return err
		}
		if i, ok := pos[id]; ok {
			out[i].Collections = append(out[i].Collections, collection)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// TasteSignals returns what a profile did with each movie and series: the history (episodes count
// for their series), then played, favorite and started.
func (q Q) TasteSignals(ctx context.Context, profileID domain.ID) ([]domain.TasteSignal, error) {
	byID := map[domain.ID]*domain.TasteSignal{}
	signal := func(id domain.ID) *domain.TasteSignal {
		s := byID[id]
		if s == nil {
			s = &domain.TasteSignal{ID: id}
			byID[id] = s
		}
		return s
	}
	var (
		id     domain.ID
		ms, at int64
		last   sql.NullInt64
		n      int
	)
	later := func(s *domain.TasteSignal, ms int64) {
		if t := fromMillis(ms); t.After(s.LastAt) {
			s.LastAt = t
		}
	}
	if err := q.each(ctx, `SELECT CASE kind WHEN 'episode' THEN series_id ELSE item_id END AS owner, SUM(watched_ms), MAX(started_at)
		FROM play_history WHERE profile_id = ? AND kind IN ('movie', 'episode') AND owner IS NOT NULL GROUP BY owner`,
		[]any{profileID}, func(rows *sql.Rows) error {
			if err := rows.Scan(&id, &ms, &at); err != nil {
				return err
			}
			s := signal(id)
			s.Watched += time.Duration(ms) * time.Millisecond
			later(s, at)
			return nil
		}); err != nil {
		return nil, err
	}
	var played, favorite int
	var position int64
	if err := q.each(ctx, `SELECT u.item_id, u.played, u.favorite, u.position_ms, u.last_played_at
		FROM user_data u JOIN items i ON i.id = u.item_id
		WHERE u.profile_id = ? AND i.kind IN ('movie', 'series') AND (u.played = 1 OR u.favorite = 1 OR u.position_ms > 0)`,
		[]any{profileID}, func(rows *sql.Rows) error {
			if err := rows.Scan(&id, &played, &favorite, &position, &last); err != nil {
				return err
			}
			s := signal(id)
			s.Played, s.Favorite, s.Started = played == 1, favorite == 1, position > 0
			s.Watched = max(s.Watched, time.Duration(position)*time.Millisecond)
			if last.Valid {
				later(s, last.Int64)
			}
			return nil
		}); err != nil {
		return nil, err
	}
	if err := q.each(ctx, `SELECT e.series_id, COUNT(*), MAX(coalesce(u.last_played_at, 0))
		FROM user_data u JOIN episodes e ON e.item_id = u.item_id
		WHERE u.profile_id = ? AND (u.played = 1 OR u.position_ms > 0) GROUP BY e.series_id`,
		[]any{profileID}, func(rows *sql.Rows) error {
			if err := rows.Scan(&id, &n, &at); err != nil {
				return err
			}
			s := signal(id)
			s.Episodes = n
			if at > 0 {
				later(s, at)
			}
			return nil
		}); err != nil {
		return nil, err
	}
	out := make([]domain.TasteSignal, 0, len(byID))
	for _, s := range byID {
		out = append(out, *s)
	}
	return out, nil
}

// each reads all the rows of a query one by one. fn must not read the database.
func (q Q) each(ctx context.Context, query string, args []any, fn func(*sql.Rows) error) error {
	rows, err := q.db.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		if err := fn(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}
