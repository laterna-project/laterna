-- name: InsertActivity :exec
INSERT INTO activity (at, kind, warning, account_id, profile_id, item_id, message) VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: DeleteActivityBefore :execrows
DELETE FROM activity WHERE at < ?;

-- name: ListFailedJobs :many
SELECT id, kind, target, attempts, last_error, updated_at
FROM jobs
WHERE state = 'failed'
ORDER BY updated_at DESC, id DESC
LIMIT ?;

-- name: GetFailedJob :one
SELECT id, kind, target FROM jobs WHERE id = ? AND state = 'failed';

-- name: DeleteFailedJob :exec
DELETE FROM jobs WHERE id = ? AND state = 'failed';

-- name: ListAllSessions :many
SELECT s.*, a.username, CAST(coalesce(p.name, '') AS TEXT) AS profile_name
FROM sessions s
JOIN accounts a ON a.id = s.account_id
LEFT JOIN profiles p ON p.id = s.profile_id
WHERE s.expires_at > ?
ORDER BY s.last_used_at DESC, s.id;
