-- name: ListLibraryFiles :many
SELECT f.id, f.path, f.size, f.mtime, f.fingerprint, f.missing_since, f.analyzed_at,
       CAST(coalesce(s.fingerprint, '') AS TEXT) AS subtitles_fingerprint,
       CAST(coalesce(s.sidecars, '') AS TEXT) AS subtitles_sidecars,
       CAST(coalesce(t.fingerprint, '') AS TEXT) AS trickplay_fingerprint,
       CAST(coalesce(g.fingerprint, '') AS TEXT) AS segments_fingerprint,
       CAST(coalesce(g.audio, 0) AS INTEGER) AS segments_audio
FROM media_files f
LEFT JOIN subtitle_sets s ON s.file_id = f.id
LEFT JOIN trickplay t ON t.file_id = f.id
LEFT JOIN segment_scans g ON g.file_id = f.id
WHERE f.library_id = ?;

-- name: GetFile :one
SELECT * FROM media_files WHERE id = ?;

-- name: InsertFile :exec
INSERT INTO media_files (id, library_id, path, size, mtime, fingerprint, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: MoveFile :exec
UPDATE media_files SET path = ?, missing_since = NULL, updated_at = ? WHERE id = ?;

-- name: UpdateFileContent :exec
UPDATE media_files
SET size = ?, mtime = ?, fingerprint = ?, missing_since = NULL, analyzed_at = NULL, updated_at = ?
WHERE id = ?;

-- name: MarkFileMissing :exec
UPDATE media_files SET missing_since = ?, updated_at = ? WHERE id = ? AND missing_since IS NULL;

-- name: MarkFilePresent :exec
UPDATE media_files SET missing_since = NULL, updated_at = ? WHERE id = ? AND missing_since IS NOT NULL;

-- name: DeleteFile :exec
DELETE FROM media_files WHERE id = ?;

-- name: ListFilesMissingBefore :many
SELECT id FROM media_files WHERE library_id = ? AND missing_since IS NOT NULL AND missing_since < ?;

-- name: SetFileAnalysis :exec
UPDATE media_files
SET container = ?, duration_ms = ?, bitrate = ?, tags = ?, analyzed_at = ?, analysis_error = '', updated_at = ?
WHERE id = ?;

-- name: SetFileAnalysisError :exec
UPDATE media_files SET analysis_error = ?, updated_at = ? WHERE id = ?;

-- name: DeleteFileStreams :exec
DELETE FROM media_streams WHERE file_id = ?;

-- name: InsertStream :exec
INSERT INTO media_streams (file_id, idx, kind, codec, profile, language, title, is_default, is_forced,
                           is_hearing_impaired, width, height, bit_depth, frame_rate, dynamic_range,
                           pixel_format, channels, channel_layout, sample_rate, bitrate)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListFileStreams :many
SELECT * FROM media_streams WHERE file_id = ? ORDER BY idx;

-- name: DeleteFileChapters :exec
DELETE FROM chapters WHERE file_id = ?;

-- name: InsertChapter :exec
INSERT INTO chapters (file_id, idx, start_ms, end_ms, title) VALUES (?, ?, ?, ?, ?);

-- name: ListFileChapters :many
SELECT * FROM chapters WHERE file_id = ? ORDER BY idx;

-- name: ListPresentFileItems :many
SELECT f.path, i.id AS item_id, i.kind, e.season_id, e.series_id, t.album_id, t.artist_id
FROM media_files f
JOIN item_files l ON l.file_id = f.id
JOIN items i ON i.id = l.item_id
LEFT JOIN episodes e ON e.item_id = i.id
LEFT JOIN tracks t ON t.item_id = i.id
WHERE f.library_id = ? AND f.missing_since IS NULL;

-- name: UpsertTrickplay :exec
INSERT INTO trickplay (file_id, fingerprint, key, interval_ms, width, height, columns, rows, count, sheets, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (file_id) DO UPDATE SET fingerprint = excluded.fingerprint, key = excluded.key,
    interval_ms = excluded.interval_ms, width = excluded.width, height = excluded.height,
    columns = excluded.columns, rows = excluded.rows, count = excluded.count, sheets = excluded.sheets,
    created_at = excluded.created_at;

-- name: GetTrickplay :one
SELECT * FROM trickplay WHERE file_id = ?;

-- name: ListTrickplayKeys :many
SELECT file_id, key FROM trickplay WHERE key <> '';
