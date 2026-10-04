-- Music: music libraries; artists, albums and tracks are catalog items (artist <- album <- track,
-- like series <- season <- episode).
--
-- SQLite cannot alter a CHECK constraint, so libraries, items and images are rebuilt (the procedure
-- from the SQLite documentation, "ALTER TABLE", section 7). Foreign keys are turned off during the
-- rebuild: otherwise dropping the old table would cascade and delete everything that depends on it.
-- This runs outside a goose transaction (PRAGMA foreign_keys has no effect inside one); the rebuild
-- itself is a single transaction.

-- +goose NO TRANSACTION
-- +goose Up
PRAGMA foreign_keys = OFF;

-- Safety net: if foreign keys were still on, the migration stops here, before anything is dropped.
CREATE TEMP TABLE music_fk_guard (active INTEGER NOT NULL CHECK (active = 0));

INSERT INTO music_fk_guard SELECT foreign_keys FROM pragma_foreign_keys;

DROP TABLE music_fk_guard;

BEGIN;

CREATE TABLE libraries_new (
    id           BLOB PRIMARY KEY NOT NULL,
    name         TEXT NOT NULL,
    name_key     TEXT NOT NULL UNIQUE,
    kind         TEXT NOT NULL CHECK (kind IN ('movies', 'shows', 'music')),
    -- Language of the metadata (BCP 47, "en-US").
    language     TEXT NOT NULL,
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL,
    last_scan_at INTEGER
) STRICT;

INSERT INTO libraries_new (id, name, name_key, kind, language, created_at, updated_at, last_scan_at)
SELECT id, name, name_key, kind, language, created_at, updated_at, last_scan_at FROM libraries;

DROP TABLE libraries;

ALTER TABLE libraries_new RENAME TO libraries;

CREATE TABLE items_new (
    id               BLOB PRIMARY KEY NOT NULL,
    library_id       BLOB NOT NULL REFERENCES libraries (id) ON DELETE CASCADE,
    kind             TEXT NOT NULL CHECK (kind IN ('movie', 'series', 'season', 'episode', 'artist', 'album', 'track')),
    -- Season -> series, episode -> season, album -> artist, track -> album.
    parent_id        BLOB REFERENCES items (id) ON DELETE CASCADE,
    -- Groups the files of one item: folder + title (movie), folder (series), series + number
    -- (season, episode), name (artist), artist + title (album), album + disc + number (track).
    group_key        TEXT NOT NULL,
    title            TEXT NOT NULL,
    sort_title       TEXT NOT NULL,
    original_title   TEXT NOT NULL DEFAULT '',
    year             INTEGER NOT NULL DEFAULT 0,
    -- YYYY-MM-DD, empty if unknown.
    premiere_date    TEXT NOT NULL DEFAULT '',
    overview         TEXT NOT NULL DEFAULT '',
    tagline          TEXT NOT NULL DEFAULT '',
    official_rating  TEXT NOT NULL DEFAULT '',
    community_rating REAL NOT NULL DEFAULT 0,
    runtime_ms       INTEGER NOT NULL DEFAULT 0,
    metadata_at      INTEGER,
    added_at         INTEGER NOT NULL,
    updated_at       INTEGER NOT NULL,
    -- Age of the rating; NULL means unrated.
    age_rating       INTEGER CHECK (age_rating BETWEEN 0 AND 21),
    UNIQUE (library_id, group_key)
) STRICT;

-- The rowid is kept: the search index (items_fts) refers to it.
INSERT INTO items_new (rowid, id, library_id, kind, parent_id, group_key, title, sort_title, original_title, year,
                       premiere_date, overview, tagline, official_rating, community_rating, runtime_ms, metadata_at,
                       added_at, updated_at, age_rating)
SELECT rowid, id, library_id, kind, parent_id, group_key, title, sort_title, original_title, year,
       premiere_date, overview, tagline, official_rating, community_rating, runtime_ms, metadata_at,
       added_at, updated_at, age_rating
FROM items;

DROP TABLE items;

ALTER TABLE items_new RENAME TO items;

CREATE INDEX items_parent ON items (parent_id);

CREATE INDEX items_library_kind ON items (library_id, kind, sort_title);

CREATE INDEX items_added ON items (library_id, kind, added_at);

-- +goose StatementBegin
CREATE TRIGGER items_fts_insert AFTER INSERT ON items BEGIN
    INSERT INTO items_fts (rowid, title, original_title) VALUES (new.rowid, new.title, new.original_title);
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER items_fts_delete AFTER DELETE ON items BEGIN
    INSERT INTO items_fts (items_fts, rowid, title, original_title) VALUES ('delete', old.rowid, old.title, old.original_title);
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER items_fts_update AFTER UPDATE OF title, original_title ON items BEGIN
    INSERT INTO items_fts (items_fts, rowid, title, original_title) VALUES ('delete', old.rowid, old.title, old.original_title);
    INSERT INTO items_fts (rowid, title, original_title) VALUES (new.rowid, new.title, new.original_title);
END;
-- +goose StatementEnd

INSERT INTO items_fts (items_fts) VALUES ('rebuild');

-- Source "embedded": a cover extracted from an audio file, kept in the metadata folder.
CREATE TABLE images_new (
    id         BLOB PRIMARY KEY NOT NULL,
    item_id    BLOB REFERENCES items (id) ON DELETE CASCADE,
    person_id  BLOB REFERENCES people (id) ON DELETE CASCADE,
    kind       TEXT NOT NULL,
    source     TEXT NOT NULL CHECK (source IN ('local', 'remote', 'embedded')),
    path       TEXT NOT NULL,
    remote_url TEXT NOT NULL DEFAULT '',
    width      INTEGER NOT NULL DEFAULT 0,
    height     INTEGER NOT NULL DEFAULT 0,
    blurhash   TEXT NOT NULL DEFAULT '',
    -- Content hash, empty until the image is analyzed.
    hash       TEXT NOT NULL DEFAULT '',
    updated_at INTEGER NOT NULL,
    CHECK ((item_id IS NULL) <> (person_id IS NULL)),
    UNIQUE (item_id, kind),
    UNIQUE (person_id, kind)
) STRICT;

INSERT INTO images_new (id, item_id, person_id, kind, source, path, remote_url, width, height, blurhash, hash, updated_at)
SELECT id, item_id, person_id, kind, source, path, remote_url, width, height, blurhash, hash, updated_at FROM images;

DROP TABLE images;

ALTER TABLE images_new RENAME TO images;

-- Tags of the file (title, artist, album...), as JSON: lower-case keys as ffprobe gives them. Read
-- again to describe an album or an artist without running ffprobe again.
ALTER TABLE media_files ADD COLUMN tags TEXT NOT NULL DEFAULT '{}';

-- What is specific to a track; album_id and artist_id repeat the parent chain (track -> album ->
-- artist) so that lists do not have to walk it.
CREATE TABLE tracks (
    item_id    BLOB PRIMARY KEY NOT NULL REFERENCES items (id) ON DELETE CASCADE,
    album_id   BLOB NOT NULL REFERENCES items (id) ON DELETE CASCADE,
    artist_id  BLOB NOT NULL REFERENCES items (id) ON DELETE CASCADE,
    -- Disc and position on it; 0 if unknown.
    disc       INTEGER NOT NULL DEFAULT 0,
    number     INTEGER NOT NULL DEFAULT 0,
    -- Artists of the track, as the tags spell them.
    artists    TEXT NOT NULL DEFAULT '',
    -- ReplayGain: gain in dB and peak; NULL if unknown.
    track_gain REAL,
    track_peak REAL,
    album_gain REAL,
    album_peak REAL
) STRICT;

CREATE INDEX tracks_album ON tracks (album_id, disc, number);

CREATE INDEX tracks_artist ON tracks (artist_id);

COMMIT;

PRAGMA foreign_keys = ON;

-- +goose Down
-- The widened constraints stay: only music, its tables and the tags go away.
DROP TABLE tracks;

DELETE FROM libraries WHERE kind = 'music';

DELETE FROM images WHERE source = 'embedded';

ALTER TABLE media_files DROP COLUMN tags;
