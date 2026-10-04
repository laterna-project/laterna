-- Intros, credits and other skippable segments: one of each kind per file, taken from a named
-- chapter or from the audio fingerprint compared with neighboring episodes. A chapter wins.

-- +goose Up
CREATE TABLE media_segments (
    file_id  BLOB NOT NULL REFERENCES media_files (id) ON DELETE CASCADE,
    kind     TEXT NOT NULL CHECK (kind IN ('intro', 'credits', 'recap', 'preview')),
    start_ms INTEGER NOT NULL CHECK (start_ms >= 0),
    end_ms   INTEGER NOT NULL CHECK (end_ms > start_ms),
    source   TEXT NOT NULL CHECK (source IN ('chapters', 'audio')),
    PRIMARY KEY (file_id, kind)
) STRICT;

-- Search done for the content with fingerprint fingerprint, with audio or not (setting off, not an
-- episode): the scan runs it again for files whose content changed, that never had one, or whose
-- audio pass is still to do.
CREATE TABLE segment_scans (
    file_id     BLOB PRIMARY KEY NOT NULL REFERENCES media_files (id) ON DELETE CASCADE,
    fingerprint TEXT NOT NULL,
    audio       INTEGER NOT NULL CHECK (audio IN (0, 1)),
    scanned_at  INTEGER NOT NULL
) STRICT;

-- +goose Down
DROP TABLE segment_scans;

DROP TABLE media_segments;
