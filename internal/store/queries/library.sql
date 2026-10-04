-- name: InsertLibrary :exec
INSERT INTO libraries (id, name, name_key, kind, language, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: GetLibrary :one
SELECT * FROM libraries WHERE id = ?;

-- name: ListLibraries :many
SELECT * FROM libraries
ORDER BY position = 0, position,
         CASE kind WHEN 'movies' THEN 0 WHEN 'shows' THEN 1 WHEN 'music' THEN 2 WHEN 'books' THEN 3 ELSE 4 END,
         name_key;

-- name: SetLibraryPosition :exec
UPDATE libraries SET position = ? WHERE id = ?;

-- name: UpdateLibrary :exec
UPDATE libraries SET name = ?, name_key = ?, language = ?, updated_at = ? WHERE id = ?;

-- name: SetLibraryScanned :exec
UPDATE libraries SET last_scan_at = ? WHERE id = ?;

-- name: DeleteLibrary :exec
DELETE FROM libraries WHERE id = ?;

-- name: InsertLibraryPath :exec
INSERT INTO library_paths (library_id, path) VALUES (?, ?);

-- name: DeleteLibraryPaths :exec
DELETE FROM library_paths WHERE library_id = ?;

-- name: ListLibraryPaths :many
SELECT path FROM library_paths WHERE library_id = ? ORDER BY path;

-- name: ListAllLibraryPaths :many
SELECT library_id, path FROM library_paths ORDER BY path;

-- name: CountLibraryItems :many
SELECT kind, count(*) AS n FROM items WHERE library_id = ? GROUP BY kind;

-- name: ListMetadataDirs :many
SELECT path, signature FROM metadata_dirs WHERE library_id = ?;

-- name: UpsertMetadataDir :exec
INSERT INTO metadata_dirs (library_id, path, signature) VALUES (?, ?, ?)
ON CONFLICT (library_id, path) DO UPDATE SET signature = excluded.signature;

-- name: DeleteMetadataDir :exec
DELETE FROM metadata_dirs WHERE library_id = ? AND path = ?;
