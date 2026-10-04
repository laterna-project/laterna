package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/store/sqlc"
)

// Activity log.

// AddActivity appends an entry to the activity log.
func (q Q) AddActivity(ctx context.Context, a domain.Activity) error {
	message, err := json.Marshal(a.Text)
	if err != nil {
		return err
	}
	return q.q.InsertActivity(ctx, sqlc.InsertActivityParams{
		At: toMillis(a.At), Kind: string(a.Kind), Warning: toInt(a.Warning),
		AccountID: a.AccountID, ProfileID: a.ProfileID, ItemID: a.ItemID, Message: string(message),
	})
}

// ActivityQuery describes a page of the activity log, newest first.
type ActivityQuery struct {
	// Before returns entries older than this one (0 starts from the newest).
	Before int64
	// WarningsOnly keeps only what deserves attention.
	WarningsOnly bool
	// AccountID keeps only what this account did.
	AccountID *domain.ID
	Limit     int
}

// Activity reads a page of the activity log.
func (q Q) Activity(ctx context.Context, aq ActivityQuery) ([]domain.Activity, error) {
	var b query
	b.add("SELECT id, at, kind, warning, account_id, profile_id, item_id, message FROM activity")
	if aq.Before > 0 {
		b.where("id < ?", aq.Before)
	}
	if aq.WarningsOnly {
		b.where("warning = 1")
	}
	if aq.AccountID != nil {
		b.where("account_id = ?", *aq.AccountID)
	}
	b.flushWhere().add(" ORDER BY id DESC LIMIT ?", aq.Limit)
	rows, err := q.db.QueryContext(ctx, b.String(), b.args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []domain.Activity
	for rows.Next() {
		var (
			a       domain.Activity
			at      int64
			kind    string
			warning int64
			message string
		)
		if err := rows.Scan(&a.ID, &at, &kind, &warning, &a.AccountID, &a.ProfileID, &a.ItemID, &message); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(message), &a.Text); err != nil {
			return nil, fmt.Errorf("activity %d: %w", a.ID, err)
		}
		a.At, a.Kind, a.Warning = fromMillis(at), domain.ActivityKind(kind), warning == 1
		out = append(out, a)
	}
	return out, rows.Err()
}

// DeleteActivityBefore forgets the entries older than before.
func (q Q) DeleteActivityBefore(ctx context.Context, before time.Time) (int64, error) {
	return q.q.DeleteActivityBefore(ctx, toMillis(before))
}

// Failed jobs.

// FailedJob is a job that failed for good, with its last error.
type FailedJob struct {
	ID        int64
	Kind      string
	Target    string
	Attempts  int
	LastError string
	At        time.Time
}

// FailedJobs lists the failed jobs, newest first.
func (q Q) FailedJobs(ctx context.Context, limit int) ([]FailedJob, error) {
	rows, err := q.q.ListFailedJobs(ctx, int64(limit))
	if err != nil {
		return nil, err
	}
	out := make([]FailedJob, len(rows))
	for i, r := range rows {
		out[i] = FailedJob{ID: r.ID, Kind: r.Kind, Target: r.Target, Attempts: int(r.Attempts), LastError: r.LastError, At: fromMillis(r.UpdatedAt)}
	}
	return out, nil
}

// FailedJob reads a failed job (ErrNotFound if it does not exist or has not failed).
func (q Q) FailedJob(ctx context.Context, id int64) (FailedJob, error) {
	r, err := q.q.GetFailedJob(ctx, id)
	return FailedJob{ID: r.ID, Kind: r.Kind, Target: r.Target}, err
}

// DeleteFailedJob forgets a failed job.
func (q Q) DeleteFailedJob(ctx context.Context, id int64) error { return q.q.DeleteFailedJob(ctx, id) }

// Devices of every account.

// Device is an open session, with the name of its account and of the profile picked on it.
type Device struct {
	Session     domain.Session
	Username    string
	ProfileName string
}

// AllSessions lists the valid sessions of every account, most recently used first.
func (q Q) AllSessions(ctx context.Context, now time.Time) ([]Device, error) {
	rows, err := q.q.ListAllSessions(ctx, toMillis(now))
	if err != nil {
		return nil, err
	}
	out := make([]Device, len(rows))
	for i, r := range rows {
		out[i] = Device{
			Session: sessionFromRow(sqlc.Session{
				ID: r.ID, AccountID: r.AccountID, ProfileID: r.ProfileID, DeviceName: r.DeviceName, Client: r.Client,
				ClientVersion: r.ClientVersion, Platform: r.Platform, CreatedAt: r.CreatedAt, LastUsedAt: r.LastUsedAt,
				ExpiresAt: r.ExpiresAt, LastIp: r.LastIp,
			}),
			Username: r.Username, ProfileName: r.ProfileName,
		}
	}
	return out, nil
}
