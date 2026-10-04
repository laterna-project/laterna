-- Subtitles extracted at import time, and attached fonts.

-- +goose Up
-- Subtitle extraction of a file: valid as long as the file's fingerprint and the signature of its
-- external files (names, sizes, dates) have not changed.
CREATE TABLE subtitle_sets (
    file_id      BLOB PRIMARY KEY NOT NULL REFERENCES media_files (id) ON DELETE CASCADE,
    fingerprint  TEXT NOT NULL,
    sidecars     TEXT NOT NULL,
    extracted_at INTEGER NOT NULL
) STRICT;

-- Subtitles of a file: its streams, then its external files.
CREATE TABLE subtitles (
    file_id             BLOB NOT NULL REFERENCES subtitle_sets (file_id) ON DELETE CASCADE,
    position            INTEGER NOT NULL,
    -- Stream of the file; -1 for an external file.
    stream_idx          INTEGER NOT NULL,
    -- Path of the external file; empty for a stream.
    path                TEXT NOT NULL,
    codec               TEXT NOT NULL,
    language            TEXT NOT NULL,
    title               TEXT NOT NULL,
    is_default          INTEGER NOT NULL CHECK (is_default IN (0, 1)),
    is_forced           INTEGER NOT NULL CHECK (is_forced IN (0, 1)),
    is_hearing_impaired INTEGER NOT NULL CHECK (is_hearing_impaired IN (0, 1)),
    -- Extracted formats, comma separated ("ass,vtt").
    formats             TEXT NOT NULL,
    width               INTEGER NOT NULL,
    height              INTEGER NOT NULL,
    PRIMARY KEY (file_id, position)
) STRICT;

-- Fonts attached to files, stored by content hash.
CREATE TABLE fonts (
    sha256 TEXT PRIMARY KEY NOT NULL,
    -- Lower-case names, separated by newlines.
    names  TEXT NOT NULL,
    ext    TEXT NOT NULL,
    size   INTEGER NOT NULL
) STRICT;

CREATE TABLE file_fonts (
    file_id BLOB NOT NULL REFERENCES subtitle_sets (file_id) ON DELETE CASCADE,
    sha256  TEXT NOT NULL REFERENCES fonts (sha256),
    PRIMARY KEY (file_id, sha256)
) STRICT;

CREATE INDEX file_fonts_by_font ON file_fonts (sha256);

-- +goose Down
DROP TABLE file_fonts;
DROP TABLE fonts;
DROP TABLE subtitles;
DROP TABLE subtitle_sets;
