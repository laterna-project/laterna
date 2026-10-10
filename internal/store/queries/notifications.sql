-- Notifications of a profile (docs/design/notifications.md). Listing, marking and deleting by ID are
-- built in notifications.go.

-- name: InsertNotification :exec
INSERT INTO notifications (id, profile_id, kind, text, item_id, request_id, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: CountUnreadNotifications :one
SELECT count(*) FROM notifications WHERE profile_id = ? AND read_at IS NULL;

-- name: NotificationAt :one
-- The ID of the notification of a profile at a rank, newest first (rank 0).
SELECT id FROM notifications WHERE profile_id = ? ORDER BY id DESC LIMIT 1 OFFSET ?;

-- name: DeleteNotificationsUpTo :exec
DELETE FROM notifications WHERE profile_id = ? AND id <= ?;

-- name: DeleteNotificationsBefore :execrows
DELETE FROM notifications WHERE created_at < ?;

-- name: AdministratorProfiles :many
-- Profiles that can administer: those of enabled administrator accounts, without restriction.
SELECT p.id
FROM profiles p
JOIN accounts a ON a.id = p.account_id
WHERE a.is_admin = 1 AND a.disabled = 0 AND p.kid = 0 AND p.max_age IS NULL AND p.block_unrated = 0
ORDER BY p.id;

-- name: SeriesFollowers :many
-- Profiles that follow a series: it is one of their favorites, or they played or started one of its
-- episodes.
SELECT u.profile_id
FROM user_data u
WHERE u.item_id = sqlc.arg(series_id) AND u.favorite = 1
UNION
SELECT u.profile_id
FROM user_data u
JOIN episodes e ON e.item_id = u.item_id
WHERE e.series_id = sqlc.arg(series_id) AND (u.played = 1 OR u.position_ms > 0)
ORDER BY 1;
