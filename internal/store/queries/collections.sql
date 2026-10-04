-- name: InsertCollection :exec
INSERT INTO collections (id, name, overview, nfo_key, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?);

-- name: GetCollection :one
SELECT * FROM collections WHERE id = ?;

-- name: GetCollectionByNFOKey :one
SELECT * FROM collections WHERE nfo_key = ?;

-- name: UpdateCollection :exec
UPDATE collections SET name = ?, overview = ?, updated_at = ? WHERE id = ?;

-- name: DeleteCollection :exec
DELETE FROM collections WHERE id = ?;

-- name: InsertCollectionItem :exec
INSERT INTO collection_items (collection_id, item_id, added_at) VALUES (?, ?, ?) ON CONFLICT DO NOTHING;

-- name: DeleteCollectionItem :exec
DELETE FROM collection_items WHERE collection_id = ? AND item_id = ?;

-- name: DeleteItemFromOtherNFOCollections :exec
DELETE FROM collection_items
WHERE item_id = sqlc.arg(item_id)
  AND collection_id IN (SELECT c.id FROM collections c WHERE c.nfo_key <> '' AND c.nfo_key <> sqlc.arg(keep_key));

-- name: DeleteEmptyNFOCollections :execrows
DELETE FROM collections
WHERE nfo_key <> '' AND NOT EXISTS (SELECT 1 FROM collection_items ci WHERE ci.collection_id = collections.id);

-- name: ListItemCollections :many
SELECT c.* FROM collections c JOIN collection_items ci ON ci.collection_id = c.id WHERE ci.item_id = ? ORDER BY c.name;

-- name: InsertPlaylist :exec
INSERT INTO playlists (id, profile_id, name, created_at, updated_at) VALUES (?, ?, ?, ?, ?);

-- name: GetPlaylist :one
SELECT * FROM playlists WHERE id = ?;

-- name: ListProfilePlaylists :many
SELECT * FROM playlists WHERE profile_id = ? ORDER BY updated_at DESC, id;

-- name: CountProfilePlaylists :one
SELECT count(*) FROM playlists WHERE profile_id = ?;

-- name: RenamePlaylist :exec
UPDATE playlists SET name = ?, updated_at = ? WHERE id = ?;

-- name: TouchPlaylist :exec
UPDATE playlists SET updated_at = ? WHERE id = ?;

-- name: DeletePlaylist :exec
DELETE FROM playlists WHERE id = ?;

-- name: ListPlaylistEntries :many
SELECT id, item_id, position FROM playlist_entries WHERE playlist_id = ? ORDER BY position;

-- name: InsertPlaylistEntry :exec
INSERT INTO playlist_entries (id, playlist_id, item_id, position, added_at) VALUES (?, ?, ?, ?, ?);

-- name: SetPlaylistEntryPosition :exec
UPDATE playlist_entries SET position = ? WHERE id = ?;

-- name: DeletePlaylistEntry :exec
DELETE FROM playlist_entries WHERE id = ? AND playlist_id = ?;
