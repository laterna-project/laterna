package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/store/sqlc"
)

// Import from another server.

// AddImportedPlay stores an imported play, unless one with the same key was already stored for this
// profile. added reports whether it is new.
func (q Q) AddImportedPlay(ctx context.Context, p domain.Play, key string) (added bool, err error) {
	n, err := q.q.InsertImportedPlay(ctx, sqlc.InsertImportedPlayParams{
		ID: p.ID, ProfileID: p.ProfileID, ItemID: p.ItemID, Kind: string(p.Kind), SeriesID: p.SeriesID, AlbumID: p.AlbumID,
		ArtistID: p.ArtistID, Title: p.Title, Subtitle: p.Subtitle, StartedAt: toMillis(p.StartedAt), EndedAt: toMillis(p.EndedAt),
		WatchedMs: p.Watched.Milliseconds(), PositionMs: p.Position.Milliseconds(), DurationMs: p.Duration.Milliseconds(),
		Completed: toInt(p.Completed), Device: p.Device, Offline: toInt(p.Offline),
		ImportKey: sql.NullString{String: key, Valid: true},
	})
	return n > 0, err
}

// UserData returns what a profile did with an item; ok is false if it did nothing.
func (q Q) UserData(ctx context.Context, profileID, itemID domain.ID) (d domain.UserData, ok bool, err error) {
	row, err := q.q.GetUserData(ctx, sqlc.GetUserDataParams{ProfileID: profileID, ItemID: itemID})
	if IsNotFound(err) {
		return domain.UserData{}, false, nil
	}
	if err != nil {
		return domain.UserData{}, false, err
	}
	return domain.UserData{
		Played: row.Played == 1, PlayCount: int(row.PlayCount), Position: time.Duration(row.PositionMs) * time.Millisecond,
		LastPlayedAt: optTime(row.LastPlayedAt), Favorite: row.Favorite == 1,
	}, true, nil
}

// SetUserData replaces what a profile did with an item.
func (q Q) SetUserData(ctx context.Context, profileID, itemID domain.ID, d domain.UserData, now time.Time) error {
	var last sql.NullInt64
	if d.LastPlayedAt != nil {
		last = sql.NullInt64{Int64: toMillis(*d.LastPlayedAt), Valid: true}
	}
	return q.q.UpsertUserData(ctx, sqlc.UpsertUserDataParams{
		ProfileID: profileID, ItemID: itemID, Played: toInt(d.Played), PlayCount: int64(d.PlayCount),
		PositionMs: d.Position.Milliseconds(), LastPlayedAt: last, Favorite: toInt(d.Favorite), UpdatedAt: toMillis(now),
	})
}
