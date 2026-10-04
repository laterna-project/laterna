-- name: GetKeyframes :one
SELECT times FROM keyframes WHERE file_id = ? AND fingerprint = ?;

-- name: UpsertKeyframes :exec
INSERT INTO keyframes (file_id, fingerprint, times, created_at) VALUES (?, ?, ?, ?)
ON CONFLICT (file_id) DO UPDATE
SET fingerprint = excluded.fingerprint, times = excluded.times, created_at = excluded.created_at;
