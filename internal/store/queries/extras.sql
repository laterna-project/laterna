-- name: UpsertExtra :exec
INSERT INTO extras (item_id, kind) VALUES (?, ?)
ON CONFLICT (item_id) DO UPDATE SET kind = excluded.kind;

-- name: GetExtra :one
SELECT * FROM extras WHERE item_id = ?;

-- name: CountExtras :one
SELECT count(*) FROM items WHERE parent_id = ? AND kind = 'extra' AND present = 1;

-- name: FirstTrailer :one
SELECT i.id FROM items i JOIN extras x ON x.item_id = i.id
WHERE i.parent_id = ? AND i.kind = 'extra' AND i.present = 1 AND x.kind = 'trailer'
ORDER BY i.sort_title, i.id
LIMIT 1;

-- name: ListMovieFilesUnder :many
SELECT i.id AS item_id, f.path FROM items i
JOIN item_files l ON l.item_id = i.id
JOIN media_files f ON f.id = l.file_id
WHERE i.library_id = ? AND i.kind = 'movie' AND f.path >= sqlc.arg(from_path) AND f.path < sqlc.arg(to_path)
  AND f.missing_since IS NULL;

-- name: ListFilesUnder :many
SELECT f.id, f.path, CAST(coalesce(i.kind, '') AS TEXT) AS item_kind FROM media_files f
LEFT JOIN item_files l ON l.file_id = f.id
LEFT JOIN items i ON i.id = l.item_id
WHERE f.library_id = ? AND f.path >= sqlc.arg(from_path) AND f.path < sqlc.arg(to_path) AND f.missing_since IS NULL;
