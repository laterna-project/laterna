-- name: GetSubtitleSet :one
SELECT * FROM subtitle_sets WHERE file_id = ?;

-- name: UpsertSubtitleSet :exec
INSERT INTO subtitle_sets (file_id, fingerprint, sidecars, extracted_at) VALUES (?, ?, ?, ?)
ON CONFLICT (file_id) DO UPDATE
SET fingerprint = excluded.fingerprint, sidecars = excluded.sidecars, extracted_at = excluded.extracted_at;

-- name: DeleteSubtitles :exec
DELETE FROM subtitles WHERE file_id = ?;

-- name: InsertSubtitle :exec
INSERT INTO subtitles (file_id, position, stream_idx, path, codec, language, title, is_default, is_forced,
                       is_hearing_impaired, formats, width, height)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListSubtitles :many
SELECT * FROM subtitles WHERE file_id = ? ORDER BY position;

-- name: InsertFont :exec
INSERT INTO fonts (sha256, names, ext, size) VALUES (?, ?, ?, ?) ON CONFLICT (sha256) DO NOTHING;

-- name: GetFont :one
SELECT * FROM fonts WHERE sha256 = ?;

-- name: DeleteFileFonts :exec
DELETE FROM file_fonts WHERE file_id = ?;

-- name: InsertFileFont :exec
INSERT INTO file_fonts (file_id, sha256) VALUES (?, ?) ON CONFLICT DO NOTHING;

-- name: ListFileFonts :many
SELECT f.sha256, f.names, f.ext, f.size
FROM fonts f JOIN file_fonts ff ON ff.sha256 = f.sha256
WHERE ff.file_id = ?
ORDER BY f.sha256;

-- name: DeleteOrphanFonts :many
DELETE FROM fonts
WHERE NOT EXISTS (SELECT 1 FROM file_fonts ff WHERE ff.sha256 = fonts.sha256)
RETURNING sha256, ext;

-- name: ListSubtitleSetFiles :many
SELECT file_id FROM subtitle_sets;
