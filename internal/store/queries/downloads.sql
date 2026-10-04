-- Offline downloads.

-- name: InsertDownload :exec
INSERT INTO downloads (id, account_id, profile_id, session_id, item_id, file_id, quality, state, plan, progress,
                       estimate, size, path, error, created_at, updated_at, ready_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetDownload :one
SELECT * FROM downloads WHERE id = ?;

-- name: ListSessionDownloads :many
SELECT * FROM downloads WHERE session_id = ? AND profile_id = ? ORDER BY created_at, id;

-- name: CountSessionDownloads :one
SELECT count(*) FROM downloads WHERE session_id = ?;

-- name: UpdateDownloadState :exec
UPDATE downloads
SET state = ?, progress = ?, size = ?, path = ?, error = ?, ready_at = ?, updated_at = ?
WHERE id = ?;

-- name: UpdateDownloadProgress :exec
UPDATE downloads SET progress = ?, updated_at = ? WHERE id = ? AND state = 'preparing';

-- name: DeleteDownload :exec
DELETE FROM downloads WHERE id = ?;

-- name: DeleteOldDownloads :execrows
DELETE FROM downloads
WHERE (state = 'ready' AND ready_at < sqlc.arg(cutoff)) OR (state = 'failed' AND updated_at < sqlc.arg(cutoff));

-- name: ListDownloadIDs :many
SELECT id FROM downloads;

-- name: SaveOfflineFinished :exec
INSERT INTO user_data (profile_id, item_id, played, play_count, position_ms, last_played_at, updated_at)
VALUES (?, ?, 1, 1, 0, ?, ?)
ON CONFLICT (profile_id, item_id) DO UPDATE
SET played = 1,
    play_count = user_data.play_count + 1,
    position_ms = CASE WHEN excluded.last_played_at >= coalesce(user_data.last_played_at, 0) THEN 0 ELSE user_data.position_ms END,
    last_played_at = max(coalesce(user_data.last_played_at, 0), excluded.last_played_at),
    updated_at = excluded.updated_at;

-- name: InsertOfflinePlay :execrows
INSERT INTO offline_plays (profile_id, item_id, played_at) VALUES (?, ?, ?) ON CONFLICT DO NOTHING;

-- name: DeleteOfflinePlaysBefore :exec
DELETE FROM offline_plays WHERE played_at < ?;

-- name: SaveOfflinePosition :exec
INSERT INTO user_data (profile_id, item_id, position_ms, last_played_at, updated_at) VALUES (?, ?, ?, ?, ?)
ON CONFLICT (profile_id, item_id) DO UPDATE
SET position_ms = excluded.position_ms,
    last_played_at = excluded.last_played_at,
    updated_at = excluded.updated_at
WHERE excluded.last_played_at > coalesce(user_data.last_played_at, 0);
