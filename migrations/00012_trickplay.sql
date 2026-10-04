-- Scrubbing thumbnails (trickplay).

-- +goose Up
-- Sheets of a video file, stored in metadata/trickplay/<first 2 chars>/<file>/<key>. Valid as long
-- as the file's fingerprint has not changed; key is empty and sheets = 0 for a file without video
-- (nothing to redo).
CREATE TABLE trickplay (
    file_id     BLOB PRIMARY KEY NOT NULL REFERENCES media_files (id) ON DELETE CASCADE,
    fingerprint TEXT NOT NULL,
    key         TEXT NOT NULL,
    interval_ms INTEGER NOT NULL,
    width       INTEGER NOT NULL,
    height      INTEGER NOT NULL,
    columns     INTEGER NOT NULL,
    rows        INTEGER NOT NULL,
    count       INTEGER NOT NULL,
    sheets      INTEGER NOT NULL,
    created_at  INTEGER NOT NULL
) STRICT;

-- +goose Down
DROP TABLE trickplay;
