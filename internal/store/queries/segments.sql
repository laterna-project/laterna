-- name: DeleteFileSegments :exec
DELETE FROM media_segments WHERE file_id = ?;

-- name: DeleteFileSegmentsFrom :exec
DELETE FROM media_segments WHERE file_id = ? AND source = ?;

-- name: UpsertChapterSegment :exec
INSERT INTO media_segments (file_id, kind, start_ms, end_ms, source) VALUES (?, ?, ?, ?, 'chapters')
ON CONFLICT (file_id, kind) DO UPDATE SET start_ms = excluded.start_ms, end_ms = excluded.end_ms, source = 'chapters';

-- name: UpsertAudioSegment :exec
INSERT INTO media_segments (file_id, kind, start_ms, end_ms, source) VALUES (?, ?, ?, ?, 'audio')
ON CONFLICT (file_id, kind) DO UPDATE SET start_ms = excluded.start_ms, end_ms = excluded.end_ms
WHERE media_segments.source = 'audio';

-- name: ListFileSegments :many
SELECT kind, start_ms, end_ms, source FROM media_segments WHERE file_id = ? ORDER BY start_ms;

-- name: UpsertSegmentScan :exec
INSERT INTO segment_scans (file_id, fingerprint, audio, scanned_at) VALUES (?, ?, ?, ?)
ON CONFLICT (file_id) DO UPDATE SET fingerprint = excluded.fingerprint, audio = excluded.audio,
                                    scanned_at = excluded.scanned_at;

-- name: ListSeasonEpisodeFiles :many
SELECT e.item_id, e.number, l.file_id
FROM episodes e
JOIN item_files l ON l.item_id = e.item_id
JOIN media_files m ON m.id = l.file_id
WHERE e.season_id = ? AND m.missing_since IS NULL AND m.analyzed_at IS NOT NULL
ORDER BY e.number, l.part, l.file_id;

-- name: GetFileEpisode :one
SELECT e.item_id, e.season_id, e.number
FROM episodes e
JOIN item_files l ON l.item_id = e.item_id
WHERE l.file_id = ?;
