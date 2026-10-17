-- name: GetSeriesChoice :one
SELECT * FROM series_choices WHERE profile_id = ? AND series_id = ?;

-- name: UpsertSeriesChoice :exec
INSERT INTO series_choices (profile_id, series_id, audio_language, subtitle_chosen, subtitle_language, subtitle_forced,
                            updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (profile_id, series_id) DO UPDATE
SET audio_language = excluded.audio_language, subtitle_chosen = excluded.subtitle_chosen,
    subtitle_language = excluded.subtitle_language, subtitle_forced = excluded.subtitle_forced,
    updated_at = excluded.updated_at;
