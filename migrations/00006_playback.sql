-- Playback: keyframe index.

-- +goose Up
-- Keyframes of the video of a file (presentation times): HLS playback is cut into segments on them.
-- Valid as long as the file's fingerprint has not changed.
CREATE TABLE keyframes (
    file_id     BLOB PRIMARY KEY NOT NULL REFERENCES media_files (id) ON DELETE CASCADE,
    fingerprint TEXT NOT NULL,
    -- Successive deltas in microseconds, varint encoded, in base64.
    times       TEXT NOT NULL,
    created_at  INTEGER NOT NULL
) STRICT;

-- +goose Down
DROP TABLE keyframes;
