-- Photos: what is specific to a photo, taken from its EXIF data. Library kinds and item kinds were
-- widened by 00015.

-- +goose Up
CREATE TABLE photos (
    item_id       BLOB PRIMARY KEY NOT NULL REFERENCES items (id) ON DELETE CASCADE,
    -- Time the picture was taken (ms); camera offset (minutes), NULL if unknown.
    taken_at      INTEGER NOT NULL,
    utc_offset    INTEGER,
    -- Dimensions as displayed (orientation applied).
    width         INTEGER NOT NULL DEFAULT 0,
    height        INTEGER NOT NULL DEFAULT 0,
    make          TEXT NOT NULL DEFAULT '',
    model         TEXT NOT NULL DEFAULT '',
    lens          TEXT NOT NULL DEFAULT '',
    f_number      REAL NOT NULL DEFAULT 0,
    exposure_time TEXT NOT NULL DEFAULT '',
    iso           INTEGER NOT NULL DEFAULT 0,
    focal_length  REAL NOT NULL DEFAULT 0,
    latitude      REAL,
    longitude     REAL
) STRICT;

-- Timeline: most recent first.
CREATE INDEX photos_taken ON photos (taken_at);

-- +goose Down
DROP TABLE photos;

DELETE FROM libraries WHERE kind = 'photos';
