package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/store/sqlc"
)

// Notifications of a profile (docs/design/notifications.md). Their IDs are UUIDv7: the order of the
// IDs is the order of creation, which is what lists and cursors use.

// AddNotification stores a notification and keeps the newest keep ones of its profile.
func (q Q) AddNotification(ctx context.Context, n domain.Notification, keep int) error {
	text, err := json.Marshal(n.Text)
	if err != nil {
		return err
	}
	if err := q.q.InsertNotification(ctx, sqlc.InsertNotificationParams{
		ID: n.ID, ProfileID: n.ProfileID, Kind: string(n.Kind), Text: string(text),
		ItemID: n.ItemID, RequestID: n.RequestID, CreatedAt: toMillis(n.CreatedAt),
	}); err != nil {
		return err
	}
	oldest, err := q.q.NotificationAt(ctx, sqlc.NotificationAtParams{ProfileID: n.ProfileID, Offset: int64(keep)})
	if IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return q.q.DeleteNotificationsUpTo(ctx, sqlc.DeleteNotificationsUpToParams{ProfileID: n.ProfileID, ID: oldest})
}

// Notifications lists the notifications of a profile, newest first, starting after one of them
// (nil: from the newest).
func (q Q) Notifications(ctx context.Context, profileID domain.ID, after *domain.ID, limit int) ([]domain.Notification, error) {
	var b query
	b.add("SELECT id, profile_id, kind, text, item_id, request_id, created_at, read_at FROM notifications")
	b.where("profile_id = ?", profileID)
	if after != nil {
		b.where("id < ?", *after)
	}
	b.flushWhere().add(" ORDER BY id DESC LIMIT ?", limit)
	rows, err := q.db.QueryContext(ctx, b.String(), b.args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []domain.Notification
	for rows.Next() {
		var (
			n       domain.Notification
			kind    string
			text    string
			created int64
			read    sql.NullInt64
		)
		if err := rows.Scan(&n.ID, &n.ProfileID, &kind, &text, &n.ItemID, &n.RequestID, &created, &read); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(text), &n.Text); err != nil {
			return nil, fmt.Errorf("notification %s: %w", n.ID, err)
		}
		n.Kind, n.CreatedAt = domain.NotificationKind(kind), fromMillis(created)
		if read.Valid {
			at := fromMillis(read.Int64)
			n.ReadAt = &at
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// UnreadNotifications counts the notifications a profile has not read.
func (q Q) UnreadNotifications(ctx context.Context, profileID domain.ID) (int, error) {
	n, err := q.q.CountUnreadNotifications(ctx, profileID)
	return int(n), err
}

// MarkNotificationsRead marks notifications of a profile as read: those named, or all of them when
// ids is nil. It returns how many changed.
func (q Q) MarkNotificationsRead(ctx context.Context, profileID domain.ID, ids []domain.ID, now time.Time) (int64, error) {
	var b query
	b.add("UPDATE notifications SET read_at = ?", toMillis(now))
	b.where("profile_id = ?", profileID).where("read_at IS NULL")
	return q.execOn(ctx, &b, ids)
}

// DeleteNotifications removes notifications of a profile: those named, or all of them when ids is
// nil. It returns how many were removed.
func (q Q) DeleteNotifications(ctx context.Context, profileID domain.ID, ids []domain.ID) (int64, error) {
	var b query
	b.add("DELETE FROM notifications")
	b.where("profile_id = ?", profileID)
	return q.execOn(ctx, &b, ids)
}

// execOn runs a statement on the rows with these IDs (every row when ids is nil) and returns how
// many it touched.
func (q Q) execOn(ctx context.Context, b *query, ids []domain.ID) (int64, error) {
	if ids != nil {
		if len(ids) == 0 {
			return 0, nil
		}
		args := make([]any, len(ids))
		for i, id := range ids {
			args[i] = id
		}
		b.where("id IN ("+placeholders(len(args))+")", args...)
	}
	b.flushWhere()
	res, err := q.db.ExecContext(ctx, b.String(), b.args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeleteNotificationsBefore forgets the notifications created before a moment.
func (q Q) DeleteNotificationsBefore(ctx context.Context, before time.Time) (int64, error) {
	return q.q.DeleteNotificationsBefore(ctx, toMillis(before))
}

// AdministratorProfiles lists the profiles that can administer the server: those of enabled
// administrator accounts that carry no restriction.
func (q Q) AdministratorProfiles(ctx context.Context) ([]domain.ID, error) {
	return q.q.AdministratorProfiles(ctx)
}

// SeriesFollowers lists the profiles that follow a series: it is one of their favorites, or they
// played or started one of its episodes.
func (q Q) SeriesFollowers(ctx context.Context, seriesID domain.ID) ([]domain.ID, error) {
	return q.q.SeriesFollowers(ctx, seriesID)
}
