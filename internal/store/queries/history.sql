-- name: InsertPlay :exec
INSERT INTO play_history (id, profile_id, item_id, kind, series_id, album_id, artist_id, title, subtitle, started_at, ended_at,
                          watched_ms, position_ms, duration_ms, completed, device, offline)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListPlays :many
SELECT * FROM play_history
WHERE profile_id = sqlc.arg(profile_id)
  AND (started_at < sqlc.arg(before_at) OR (started_at = sqlc.arg(before_at) AND id < sqlc.arg(before_id)))
ORDER BY started_at DESC, id DESC
LIMIT sqlc.arg(lim);

-- name: DeletePlay :execrows
DELETE FROM play_history WHERE id = ? AND profile_id = ?;

-- name: DeleteProfilePlays :execrows
DELETE FROM play_history WHERE profile_id = ?;

-- name: ListGenresOfItems :many
SELECT item_id, genre FROM item_genres WHERE item_id IN (sqlc.slice(item_ids)) ORDER BY item_id, genre;

-- name: FirstPlay :one
SELECT * FROM play_history
WHERE profile_id = sqlc.arg(profile_id) AND started_at >= sqlc.arg(from_at) AND started_at < sqlc.arg(to_at)
ORDER BY started_at, id
LIMIT 1;

-- name: LastPlay :one
SELECT * FROM play_history
WHERE profile_id = sqlc.arg(profile_id) AND started_at >= sqlc.arg(from_at) AND started_at < sqlc.arg(to_at)
ORDER BY started_at DESC, id DESC
LIMIT 1;

-- name: InsertImportedPlay :execrows
INSERT INTO play_history (id, profile_id, item_id, kind, series_id, album_id, artist_id, title, subtitle, started_at, ended_at,
                          watched_ms, position_ms, duration_ms, completed, device, offline, import_key)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (profile_id, import_key) WHERE import_key IS NOT NULL DO NOTHING;
