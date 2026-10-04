package store

import (
	"context"
	"time"

	"github.com/laterna-project/laterna/internal/store/sqlc"
)

// Q gives access to the data as domain types. It comes from Store.Read (read only) or Store.Write
// (write transaction). Static SQL queries live in queries/*.sql and are compiled by sqlc; filtered
// lists are built with query.go.
type Q struct {
	q *sqlc.Queries
	// db runs built queries (a read connection or the write transaction).
	db sqlc.DBTX
}

func newQ(db sqlc.DBTX) Q { return Q{q: sqlc.New(db), db: db} }

// Schema conventions: dates are Unix milliseconds, booleans are 0/1.

func toMillis(t time.Time) int64 { return t.UnixMilli() }

func fromMillis(ms int64) time.Time { return time.UnixMilli(ms).UTC() }

func toInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// Setting reads a setting; ok is false if it is missing.
func (q Q) Setting(ctx context.Context, key string) (value string, ok bool, err error) {
	value, err = q.q.GetSetting(ctx, key)
	if IsNotFound(err) {
		return "", false, nil
	}
	return value, err == nil, err
}

// SetSetting writes a setting.
func (q Q) SetSetting(ctx context.Context, key, value string) error {
	return q.q.UpsertSetting(ctx, sqlc.UpsertSettingParams{Key: key, Value: value})
}

// InitSetting writes a setting only if it is missing.
func (q Q) InitSetting(ctx context.Context, key, value string) error {
	return q.q.InsertSettingIfAbsent(ctx, sqlc.InsertSettingIfAbsentParams{Key: key, Value: value})
}

// DeleteSetting removes a setting.
func (q Q) DeleteSetting(ctx context.Context, key string) error { return q.q.DeleteSetting(ctx, key) }

// Ping checks that the database answers.
func (q Q) Ping(ctx context.Context) error {
	_, err := q.q.Ping(ctx)
	return err
}
