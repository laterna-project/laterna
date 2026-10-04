-- Libraries, analyzed files and catalog items. Items use class-table inheritance: `items` holds
-- what all kinds share, `seasons` and `episodes` what is specific to them.

-- +goose Up
CREATE TABLE libraries (
    id           BLOB PRIMARY KEY NOT NULL,
    name         TEXT NOT NULL,
    name_key     TEXT NOT NULL UNIQUE,
    kind         TEXT NOT NULL CHECK (kind IN ('movies', 'shows')),
    -- Language of the metadata (BCP 47, "en-US").
    language     TEXT NOT NULL,
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL,
    last_scan_at INTEGER
) STRICT;

CREATE TABLE library_paths (
    library_id BLOB NOT NULL REFERENCES libraries (id) ON DELETE CASCADE,
    -- A folder belongs to one library only.
    path       TEXT NOT NULL UNIQUE,
    PRIMARY KEY (library_id, path)
) STRICT;

CREATE TABLE media_files (
    id             BLOB PRIMARY KEY NOT NULL,
    library_id     BLOB NOT NULL REFERENCES libraries (id) ON DELETE CASCADE,
    path           TEXT NOT NULL UNIQUE,
    size           INTEGER NOT NULL,
    mtime          INTEGER NOT NULL,
    -- Content fingerprint: finds a file again after it was renamed or moved.
    fingerprint    TEXT NOT NULL,
    -- First time the file was seen missing; NULL means present.
    missing_since  INTEGER,
    -- Last successful analysis; NULL means to be analyzed.
    analyzed_at    INTEGER,
    analysis_error TEXT NOT NULL DEFAULT '',
    container      TEXT NOT NULL DEFAULT '',
    duration_ms    INTEGER NOT NULL DEFAULT 0,
    bitrate        INTEGER NOT NULL DEFAULT 0,
    created_at     INTEGER NOT NULL,
    updated_at     INTEGER NOT NULL
) STRICT;

CREATE INDEX media_files_library ON media_files (library_id);

CREATE INDEX media_files_fingerprint ON media_files (library_id, fingerprint);

CREATE TABLE media_streams (
    file_id             BLOB NOT NULL REFERENCES media_files (id) ON DELETE CASCADE,
    idx                 INTEGER NOT NULL,
    kind                TEXT NOT NULL,
    codec               TEXT NOT NULL,
    profile             TEXT NOT NULL,
    language            TEXT NOT NULL,
    title               TEXT NOT NULL,
    is_default          INTEGER NOT NULL,
    is_forced           INTEGER NOT NULL,
    is_hearing_impaired INTEGER NOT NULL,
    width               INTEGER NOT NULL,
    height              INTEGER NOT NULL,
    bit_depth           INTEGER NOT NULL,
    frame_rate          REAL NOT NULL,
    dynamic_range       TEXT NOT NULL,
    pixel_format        TEXT NOT NULL,
    channels            INTEGER NOT NULL,
    channel_layout      TEXT NOT NULL,
    sample_rate         INTEGER NOT NULL,
    bitrate             INTEGER NOT NULL,
    PRIMARY KEY (file_id, idx)
) STRICT;

CREATE TABLE chapters (
    file_id  BLOB NOT NULL REFERENCES media_files (id) ON DELETE CASCADE,
    idx      INTEGER NOT NULL,
    start_ms INTEGER NOT NULL,
    end_ms   INTEGER NOT NULL,
    title    TEXT NOT NULL,
    PRIMARY KEY (file_id, idx)
) STRICT;

CREATE TABLE items (
    id               BLOB PRIMARY KEY NOT NULL,
    library_id       BLOB NOT NULL REFERENCES libraries (id) ON DELETE CASCADE,
    kind             TEXT NOT NULL CHECK (kind IN ('movie', 'series', 'season', 'episode')),
    -- Season -> series, episode -> season.
    parent_id        BLOB REFERENCES items (id) ON DELETE CASCADE,
    -- Groups the files of one item: folder + title (movie), folder (series), series + number
    -- (season, episode).
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
    UNIQUE (library_id, group_key)
) STRICT;

CREATE INDEX items_parent ON items (parent_id);

CREATE INDEX items_library_kind ON items (library_id, kind, sort_title);

CREATE INDEX items_added ON items (library_id, kind, added_at);

CREATE TABLE seasons (
    item_id   BLOB PRIMARY KEY NOT NULL REFERENCES items (id) ON DELETE CASCADE,
    series_id BLOB NOT NULL REFERENCES items (id) ON DELETE CASCADE,
    number    INTEGER NOT NULL,
    UNIQUE (series_id, number)
) STRICT;

CREATE TABLE episodes (
    item_id       BLOB PRIMARY KEY NOT NULL REFERENCES items (id) ON DELETE CASCADE,
    series_id     BLOB NOT NULL REFERENCES items (id) ON DELETE CASCADE,
    season_id     BLOB NOT NULL REFERENCES items (id) ON DELETE CASCADE,
    season_number INTEGER NOT NULL,
    number        INTEGER NOT NULL,
    -- Last episode of a multi-episode file, 0 otherwise.
    number_end    INTEGER NOT NULL DEFAULT 0,
    -- Absolute numbering (no known season), common for anime.
    absolute      INTEGER NOT NULL DEFAULT 0 CHECK (absolute IN (0, 1))
) STRICT;

CREATE INDEX episodes_series ON episodes (series_id, season_number, number);

CREATE INDEX episodes_season ON episodes (season_id, number);

CREATE TABLE item_files (
    item_id BLOB NOT NULL REFERENCES items (id) ON DELETE CASCADE,
    -- A file belongs to one item only.
    file_id BLOB NOT NULL UNIQUE REFERENCES media_files (id) ON DELETE CASCADE,
    version TEXT NOT NULL DEFAULT '',
    part    INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (item_id, file_id)
) STRICT;

CREATE TABLE provider_ids (
    item_id  BLOB NOT NULL REFERENCES items (id) ON DELETE CASCADE,
    provider TEXT NOT NULL,
    value    TEXT NOT NULL,
    PRIMARY KEY (item_id, provider)
) STRICT;

CREATE INDEX provider_ids_value ON provider_ids (provider, value);

CREATE TABLE item_genres (
    item_id BLOB NOT NULL REFERENCES items (id) ON DELETE CASCADE,
    genre   TEXT NOT NULL,
    PRIMARY KEY (item_id, genre)
) STRICT;

CREATE INDEX item_genres_genre ON item_genres (genre);

CREATE TABLE item_studios (
    item_id BLOB NOT NULL REFERENCES items (id) ON DELETE CASCADE,
    studio  TEXT NOT NULL,
    PRIMARY KEY (item_id, studio)
) STRICT;

CREATE TABLE people (
    id         BLOB PRIMARY KEY NOT NULL,
    name       TEXT NOT NULL,
    name_key   TEXT NOT NULL UNIQUE,
    created_at INTEGER NOT NULL
) STRICT;

CREATE TABLE item_people (
    item_id    BLOB NOT NULL REFERENCES items (id) ON DELETE CASCADE,
    person_id  BLOB NOT NULL REFERENCES people (id) ON DELETE CASCADE,
    role       TEXT NOT NULL CHECK (role IN ('actor', 'director', 'writer')),
    character  TEXT NOT NULL DEFAULT '',
    sort_order INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (item_id, person_id, role)
) STRICT;

CREATE INDEX item_people_person ON item_people (person_id);

CREATE TABLE images (
    id         BLOB PRIMARY KEY NOT NULL,
    item_id    BLOB REFERENCES items (id) ON DELETE CASCADE,
    person_id  BLOB REFERENCES people (id) ON DELETE CASCADE,
    kind       TEXT NOT NULL,
    source     TEXT NOT NULL CHECK (source IN ('local', 'remote')),
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

-- +goose Down
DROP TABLE images;
DROP TABLE item_people;
DROP TABLE people;
DROP TABLE item_studios;
DROP TABLE item_genres;
DROP TABLE provider_ids;
DROP TABLE item_files;
DROP TABLE episodes;
DROP TABLE seasons;
DROP TABLE items;
DROP TABLE chapters;
DROP TABLE media_streams;
DROP TABLE media_files;
DROP TABLE library_paths;
DROP TABLE libraries;
