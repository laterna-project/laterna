-- name: GetItem :one
SELECT * FROM items WHERE id = ?;

-- name: GetItemByGroupKey :one
SELECT * FROM items WHERE library_id = ? AND group_key = ?;

-- name: InsertItem :exec
INSERT INTO items (id, library_id, kind, parent_id, group_key, title, sort_title, year, added_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: SetItemGroupKey :exec
UPDATE items SET group_key = ?, updated_at = ? WHERE id = ?;

-- name: SetItemParent :exec
UPDATE items SET parent_id = ?, updated_at = ? WHERE id = ?;

-- name: UpdateItemMetadata :exec
UPDATE items
SET title = ?, sort_title = ?, original_title = ?, year = ?, premiere_date = ?, overview = ?, tagline = ?,
    official_rating = ?, age_rating = ?, community_rating = ?, runtime_ms = ?, metadata_at = ?, updated_at = ?
WHERE id = ?;

-- name: DeleteItem :exec
DELETE FROM items WHERE id = ?;

-- name: InsertSeason :exec
INSERT INTO seasons (item_id, series_id, number) VALUES (?, ?, ?);

-- name: GetSeason :one
SELECT * FROM seasons WHERE item_id = ?;

-- name: InsertEpisode :exec
INSERT INTO episodes (item_id, series_id, season_id, season_number, number, number_end, absolute)
VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: GetEpisode :one
SELECT * FROM episodes WHERE item_id = ?;

-- name: LinkFile :exec
INSERT INTO item_files (item_id, file_id, version, part) VALUES (?, ?, ?, ?)
ON CONFLICT (file_id) DO UPDATE SET item_id = excluded.item_id, version = excluded.version, part = excluded.part;

-- name: GetFileItem :one
SELECT item_id FROM item_files WHERE file_id = ?;

-- name: ListItemFiles :many
SELECT f.*, l.version, l.part
FROM item_files l JOIN media_files f ON f.id = l.file_id
WHERE l.item_id = ?
ORDER BY l.version, l.part, f.path;

-- name: DeleteOrphanLeafItems :execrows
DELETE FROM items
WHERE library_id = ? AND kind IN ('movie', 'episode', 'track', 'book', 'photo')
  AND NOT EXISTS (SELECT 1 FROM item_files l WHERE l.item_id = items.id);

-- name: DeleteEmptySeasons :execrows
DELETE FROM items
WHERE library_id = ? AND kind = 'season'
  AND NOT EXISTS (SELECT 1 FROM episodes e WHERE e.season_id = items.id);

-- name: DeleteEmptySeries :execrows
DELETE FROM items
WHERE library_id = ? AND kind = 'series'
  AND NOT EXISTS (SELECT 1 FROM seasons s WHERE s.series_id = items.id);

-- name: DeleteEmptyAlbums :execrows
DELETE FROM items
WHERE library_id = ? AND kind = 'album'
  AND NOT EXISTS (SELECT 1 FROM tracks t WHERE t.album_id = items.id);

-- name: DeleteEmptyArtists :execrows
DELETE FROM items
WHERE items.library_id = ? AND items.kind = 'artist'
  AND NOT EXISTS (SELECT 1 FROM items a WHERE a.parent_id = items.id);

-- name: DeleteProviderIDs :exec
DELETE FROM provider_ids WHERE item_id = ?;

-- name: InsertProviderID :exec
INSERT INTO provider_ids (item_id, provider, value) VALUES (?, ?, ?);

-- name: ListProviderIDs :many
SELECT provider, value FROM provider_ids WHERE item_id = ? ORDER BY provider;

-- name: DeleteItemGenres :exec
DELETE FROM item_genres WHERE item_id = ?;

-- name: InsertItemGenre :exec
INSERT INTO item_genres (item_id, genre) VALUES (?, ?) ON CONFLICT DO NOTHING;

-- name: ListItemGenres :many
SELECT genre FROM item_genres WHERE item_id = ? ORDER BY genre;

-- name: DeleteItemStudios :exec
DELETE FROM item_studios WHERE item_id = ?;

-- name: InsertItemStudio :exec
INSERT INTO item_studios (item_id, studio) VALUES (?, ?) ON CONFLICT DO NOTHING;

-- name: ListItemStudios :many
SELECT studio FROM item_studios WHERE item_id = ? ORDER BY studio;

-- name: GetPersonByKey :one
SELECT * FROM people WHERE name_key = ?;

-- name: InsertPerson :exec
INSERT INTO people (id, name, name_key, created_at) VALUES (?, ?, ?, ?);

-- name: DeleteItemPeople :exec
DELETE FROM item_people WHERE item_id = ?;

-- name: InsertItemPerson :exec
INSERT INTO item_people (item_id, person_id, role, character, sort_order) VALUES (?, ?, ?, ?, ?)
ON CONFLICT DO NOTHING;

-- name: ListItemPeople :many
SELECT p.id, p.name, ip.role, ip.character, ip.sort_order,
       pi.id AS image_id,
       CAST(coalesce(pi.width, 0) AS INTEGER) AS image_width,
       CAST(coalesce(pi.height, 0) AS INTEGER) AS image_height,
       CAST(coalesce(pi.blurhash, '') AS TEXT) AS image_blurhash,
       CAST(coalesce(pi.hash, '') AS TEXT) AS image_hash
FROM item_people ip
JOIN people p ON p.id = ip.person_id
LEFT JOIN images pi ON pi.person_id = p.id AND pi.kind = 'poster' AND pi.hash <> ''
WHERE ip.item_id = ?
ORDER BY ip.role, ip.sort_order, p.name_key;

-- name: DeleteOrphanPeople :execrows
DELETE FROM people WHERE NOT EXISTS (SELECT 1 FROM item_people ip WHERE ip.person_id = people.id);

-- name: GetPersonImage :one
SELECT * FROM images WHERE person_id = ? AND kind = ?;

-- name: ListRemoteImagePaths :many
SELECT path FROM images WHERE source IN ('remote', 'embedded');

-- name: GetItemImage :one
SELECT * FROM images WHERE item_id = ? AND kind = ?;

-- name: ListItemImages :many
SELECT * FROM images WHERE item_id = ? ORDER BY kind;

-- name: InsertImage :exec
INSERT INTO images (id, item_id, person_id, kind, source, path, remote_url, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: ReplaceImageFile :exec
UPDATE images
SET source = ?, path = ?, remote_url = ?, width = 0, height = 0, blurhash = '', hash = '', updated_at = ?
WHERE id = ?;

-- name: DeleteImage :exec
DELETE FROM images WHERE id = ?;

-- name: GetImage :one
SELECT * FROM images WHERE id = ?;

-- name: SetImageAnalysis :exec
UPDATE images SET width = ?, height = ?, blurhash = ?, hash = ?, updated_at = ? WHERE id = ?;

-- name: FirstFileOfSeries :one
SELECT f.path
FROM media_files f
JOIN item_files l ON l.file_id = f.id
JOIN episodes e ON e.item_id = l.item_id
WHERE e.series_id = ? AND f.missing_since IS NULL
ORDER BY e.season_number, e.number
LIMIT 1;

-- name: FirstFileOfSeason :one
SELECT f.path
FROM media_files f
JOIN item_files l ON l.file_id = f.id
JOIN episodes e ON e.item_id = l.item_id
WHERE e.season_id = ? AND f.missing_since IS NULL
ORDER BY e.number
LIMIT 1;

-- name: CountPresentItemsByKind :many
SELECT kind, count(*) AS n FROM items WHERE present = 1 GROUP BY kind ORDER BY kind;
