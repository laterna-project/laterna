-- Requests for movies and series (docs/design/requests.md): where they land (destinations, set by
-- an administrator), the requests themselves, and what each account may request.

-- +goose Up
CREATE TABLE request_destinations (
    id                   BLOB PRIMARY KEY NOT NULL,
    name                 TEXT NOT NULL,
    name_key             TEXT NOT NULL UNIQUE,
    kind                 TEXT NOT NULL CHECK (kind IN ('series', 'movie')),
    library_id           BLOB NOT NULL REFERENCES libraries (id) ON DELETE CASCADE,
    -- On Sonarr or Radarr: root folder, quality profile (and its name when it was chosen), and for
    -- Sonarr how episodes are numbered.
    root_folder          TEXT NOT NULL,
    quality_profile_id   INTEGER NOT NULL,
    quality_profile_name TEXT NOT NULL DEFAULT '',
    series_type          TEXT NOT NULL DEFAULT 'standard' CHECK (series_type IN ('standard', 'anime', 'daily')),
    created_at           INTEGER NOT NULL,
    updated_at           INTEGER NOT NULL
) STRICT;

CREATE TABLE requests (
    id                 BLOB PRIMARY KEY NOT NULL,
    kind               TEXT NOT NULL CHECK (kind IN ('series', 'movie')),
    -- TVDB ID of a series, TMDB ID of a movie.
    external_id        INTEGER NOT NULL,
    title              TEXT NOT NULL,
    year               INTEGER NOT NULL DEFAULT 0,
    -- Poster address on TVDB or TMDB, served through the server.
    poster             TEXT NOT NULL DEFAULT '',
    status             TEXT NOT NULL
        CHECK (status IN ('pending', 'approved', 'downloading', 'available', 'declined', 'failed')),
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
    -- The series or movie on the instance once handed over; 0 before.
    arr_id             INTEGER NOT NULL DEFAULT 0,
    progress           REAL NOT NULL DEFAULT 0,
    item_id            BLOB REFERENCES items (id) ON DELETE SET NULL,
    episodes_available INTEGER NOT NULL DEFAULT 0,
    episodes_wanted    INTEGER NOT NULL DEFAULT 0,
    available_at       INTEGER
) STRICT;

CREATE INDEX requests_created ON requests (created_at, id);

CREATE INDEX requests_profile ON requests (profile_id, created_at, id);

CREATE INDEX requests_account ON requests (account_id, created_at);

CREATE INDEX requests_status ON requests (status);

-- One open request per title.
CREATE UNIQUE INDEX requests_open ON requests (kind, external_id)
    WHERE status IN ('pending', 'approved', 'downloading');

ALTER TABLE accounts ADD COLUMN deny_requests INTEGER NOT NULL DEFAULT 0 CHECK (deny_requests IN (0, 1));
ALTER TABLE accounts ADD COLUMN auto_approve_requests INTEGER NOT NULL DEFAULT 0
    CHECK (auto_approve_requests IN (0, 1));
ALTER TABLE accounts ADD COLUMN request_quota INTEGER NOT NULL DEFAULT 10 CHECK (request_quota >= 0);

-- +goose Down
ALTER TABLE accounts DROP COLUMN request_quota;
ALTER TABLE accounts DROP COLUMN auto_approve_requests;
ALTER TABLE accounts DROP COLUMN deny_requests;
DROP TABLE requests;
DROP TABLE request_destinations;
