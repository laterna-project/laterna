-- Web push subscriptions of the devices (docs/design/notifications.md).

-- name: DeletePushEndpoint :execrows
DELETE FROM push_subscriptions WHERE endpoint = ?;

-- name: UpsertPushSubscription :exec
INSERT INTO push_subscriptions (session_id, endpoint, p256dh, auth, created_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT (session_id) DO UPDATE
SET endpoint = excluded.endpoint, p256dh = excluded.p256dh, auth = excluded.auth, created_at = excluded.created_at;

-- name: DeletePushSubscription :execrows
DELETE FROM push_subscriptions WHERE session_id = ?;

-- name: GetPushEndpoint :one
SELECT endpoint FROM push_subscriptions WHERE session_id = ?;

-- name: ProfilePushSubscriptions :many
-- Subscriptions of the devices a profile is picked on, whose session is still valid.
SELECT p.session_id, p.endpoint, p.p256dh, p.auth
FROM push_subscriptions p
JOIN sessions s ON s.id = p.session_id
WHERE s.profile_id = ? AND s.expires_at > ?
ORDER BY p.session_id;

-- name: GetNotification :one
SELECT id, profile_id, kind, text, item_id, request_id, created_at, read_at FROM notifications WHERE id = ?;
