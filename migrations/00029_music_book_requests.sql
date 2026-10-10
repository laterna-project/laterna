-- Requests for music (Lidarr) and books (LazyLibrarian), docs/design/requests.md: wider kinds, a
-- text key for the MusicBrainz and OpenLibrary IDs, a subtitle (artist, author), and Lidarr's
-- metadata profile on destinations.
--
-- SQLite cannot alter a CHECK constraint, so both tables are rebuilt (the procedure from the SQLite
-- documentation, "ALTER TABLE", section 7, as in 00013). Foreign keys are turned off during the
-- rebuild: otherwise dropping request_destinations would empty the destination of every request.
-- This runs outside a goose transaction (PRAGMA foreign_keys has no effect inside one); the rebuild
-- itself is a single transaction.

-- +goose NO TRANSACTION
-- +goose Up
PRAGMA foreign_keys = OFF;

-- Safety net: if foreign keys were still on, the migration stops here, before anything is dropped.
CREATE TEMP TABLE requests_fk_guard (active INTEGER NOT NULL CHECK (active = 0));

INSERT INTO requests_fk_guard SELECT foreign_keys FROM pragma_foreign_keys;

DROP TABLE requests_fk_guard;

BEGIN;

CREATE TABLE request_destinations_new (
    id                    BLOB PRIMARY KEY NOT NULL,
    name                  TEXT NOT NULL,
    name_key              TEXT NOT NULL UNIQUE,
    kind                  TEXT NOT NULL CHECK (kind IN ('series', 'movie', 'music', 'book')),
    library_id            BLOB NOT NULL REFERENCES libraries (id) ON DELETE CASCADE,
    -- On Sonarr, Radarr or Lidarr: root folder, quality profile (and its name when it was chosen),
    -- for Sonarr how episodes are numbered, for Lidarr the metadata profile. Empty for books.
    root_folder           TEXT NOT NULL,
    quality_profile_id    INTEGER NOT NULL,
    quality_profile_name  TEXT NOT NULL DEFAULT '',
    series_type           TEXT NOT NULL DEFAULT 'standard' CHECK (series_type IN ('standard', 'anime', 'daily')),
    metadata_profile_id   INTEGER NOT NULL DEFAULT 0,
    metadata_profile_name TEXT NOT NULL DEFAULT '',
    created_at            INTEGER NOT NULL,
    updated_at            INTEGER NOT NULL
) STRICT;

INSERT INTO request_destinations_new (id, name, name_key, kind, library_id, root_folder, quality_profile_id,
                                      quality_profile_name, series_type, created_at, updated_at)
SELECT id, name, name_key, kind, library_id, root_folder, quality_profile_id, quality_profile_name, series_type,
       created_at, updated_at
FROM request_destinations;

DROP TABLE request_destinations;

ALTER TABLE request_destinations_new RENAME TO request_destinations;

CREATE TABLE requests_new (
    id                 BLOB PRIMARY KEY NOT NULL,
    kind               TEXT NOT NULL CHECK (kind IN ('series', 'movie', 'artist', 'album', 'book')),
    -- TVDB ID of a series, TMDB ID of a movie; 0 for the others.
    external_id        INTEGER NOT NULL,
    -- MusicBrainz ID of an artist or a release group, OpenLibrary work ID of a book; '' for the
    -- others.
    external_key       TEXT NOT NULL DEFAULT '',
    title              TEXT NOT NULL,
    -- Artist of an album, author of a book; '' otherwise.
    subtitle           TEXT NOT NULL DEFAULT '',
    year               INTEGER NOT NULL DEFAULT 0,
    -- Poster address on TVDB, TMDB, MusicBrainz or OpenLibrary, served through the server.
    poster             TEXT NOT NULL DEFAULT '',
    status             TEXT NOT NULL
        CHECK (status IN ('pending', 'approved', 'downloading', 'available', 'declined', 'failed')),
    -- Seasons of a series, albums of an artist.
    seasons            TEXT NOT NULL DEFAULT 'all' CHECK (seasons IN ('all', 'first', 'latest', 'chosen')),
    -- Chosen seasons, as a JSON array of numbers.
    season_numbers     TEXT NOT NULL DEFAULT '[]',
    destination_id     BLOB REFERENCES request_destinations (id) ON DELETE SET NULL,
    account_id         BLOB NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    profile_id         BLOB NOT NULL REFERENCES profiles (id) ON DELETE CASCADE,
    created_at         INTEGER NOT NULL,
    updated_at         INTEGER NOT NULL,
    decided_at         INTEGER,
    decided_by         TEXT NOT NULL DEFAULT '',
    decline_reason     TEXT NOT NULL DEFAULT '',
    -- Why it failed, as composed text (JSON); empty otherwise.
    error              TEXT NOT NULL DEFAULT '',
    -- The series, movie, artist or album on the instance once handed over; 0 before (and for
    -- books: LazyLibrarian's IDs are the external key).
    arr_id             INTEGER NOT NULL DEFAULT 0,
    progress           REAL NOT NULL DEFAULT 0,
    item_id            BLOB REFERENCES items (id) ON DELETE SET NULL,
    -- Episodes of a series, tracks of an artist or an album.
    episodes_available INTEGER NOT NULL DEFAULT 0,
    episodes_wanted    INTEGER NOT NULL DEFAULT 0,
    available_at       INTEGER
) STRICT;

INSERT INTO requests_new (id, kind, external_id, title, year, poster, status, seasons, season_numbers,
                          destination_id, account_id, profile_id, created_at, updated_at, decided_at, decided_by,
                          decline_reason, error, arr_id, progress, item_id, episodes_available, episodes_wanted,
                          available_at)
SELECT id, kind, external_id, title, year, poster, status, seasons, season_numbers, destination_id, account_id,
       profile_id, created_at, updated_at, decided_at, decided_by, decline_reason, error, arr_id, progress, item_id,
       episodes_available, episodes_wanted, available_at
FROM requests;

DROP TABLE requests;

ALTER TABLE requests_new RENAME TO requests;

CREATE INDEX requests_created ON requests (created_at, id);

CREATE INDEX requests_profile ON requests (profile_id, created_at, id);

CREATE INDEX requests_account ON requests (account_id, created_at);

CREATE INDEX requests_status ON requests (status);

-- One open request per title.
CREATE UNIQUE INDEX requests_open ON requests (kind, external_id, external_key)
    WHERE status IN ('pending', 'approved', 'downloading');

COMMIT;

PRAGMA foreign_keys = ON;

-- +goose Down
-- The widened constraints stay: music and book requests and destinations go, then the new columns.
DELETE FROM requests WHERE kind NOT IN ('series', 'movie');

DELETE FROM request_destinations WHERE kind NOT IN ('series', 'movie');

DROP INDEX requests_open;

ALTER TABLE requests DROP COLUMN external_key;

ALTER TABLE requests DROP COLUMN subtitle;

CREATE UNIQUE INDEX requests_open ON requests (kind, external_id)
    WHERE status IN ('pending', 'approved', 'downloading');

ALTER TABLE request_destinations DROP COLUMN metadata_profile_name;

ALTER TABLE request_destinations DROP COLUMN metadata_profile_id;
