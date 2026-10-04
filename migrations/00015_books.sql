-- Books: book and photo libraries; book series, books, photo albums and photos among the items;
-- illustrators among the roles; what is specific to books.
--
-- As for music (00013): libraries, items and item_people are rebuilt, with foreign keys off during
-- the rebuild, outside a goose transaction.

-- +goose NO TRANSACTION
-- +goose Up
PRAGMA foreign_keys = OFF;

-- Safety net: if foreign keys were still on, the migration stops here, before anything is dropped.
CREATE TEMP TABLE books_fk_guard (active INTEGER NOT NULL CHECK (active = 0));

INSERT INTO books_fk_guard SELECT foreign_keys FROM pragma_foreign_keys;

DROP TABLE books_fk_guard;

BEGIN;

CREATE TABLE libraries_new (
    id           BLOB PRIMARY KEY NOT NULL,
    name         TEXT NOT NULL,
    name_key     TEXT NOT NULL UNIQUE,
    kind         TEXT NOT NULL CHECK (kind IN ('movies', 'shows', 'music', 'books', 'photos')),
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
    kind             TEXT NOT NULL CHECK (kind IN ('movie', 'series', 'season', 'episode', 'artist', 'album', 'track',
                                                   'book_series', 'book', 'photo_album', 'photo')),
    -- Season -> series, episode -> season, album -> artist, track -> album, book -> book series,
    -- photo -> photo album, photo album -> the album that contains it.
    parent_id        BLOB REFERENCES items (id) ON DELETE CASCADE,
    -- Groups the files of one item: folder + title (movie), folder (series), series + number
    -- (season, episode), name (artist), artist + title (album), album + disc + number (track), name
    -- (book series), series + volume or title (book), folder (photo album), file (photo).
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

-- "illustrator": the artist of a comic.
CREATE TABLE item_people_new (
    item_id    BLOB NOT NULL REFERENCES items (id) ON DELETE CASCADE,
    person_id  BLOB NOT NULL REFERENCES people (id) ON DELETE CASCADE,
    role       TEXT NOT NULL CHECK (role IN ('actor', 'director', 'writer', 'illustrator')),
    character  TEXT NOT NULL DEFAULT '',
    sort_order INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (item_id, person_id, role)
) STRICT;

INSERT INTO item_people_new (item_id, person_id, role, character, sort_order)
SELECT item_id, person_id, role, character, sort_order FROM item_people;

DROP TABLE item_people;

ALTER TABLE item_people_new RENAME TO item_people;

CREATE INDEX item_people_person ON item_people (person_id);

-- What is specific to a book; its series is its parent.
CREATE TABLE books (
    item_id   BLOB PRIMARY KEY NOT NULL REFERENCES items (id) ON DELETE CASCADE,
    -- Volume in its series (0.5 or 12.5 happen); 0 if standalone or unknown.
    number    REAL NOT NULL DEFAULT 0,
    publisher TEXT NOT NULL DEFAULT '',
    -- Language of the text (BCP 47); empty if unknown.
    language  TEXT NOT NULL DEFAULT '',
    isbn      TEXT NOT NULL DEFAULT ''
) STRICT;

-- How to read a book file, decided at analysis time.
CREATE TABLE book_files (
    file_id       BLOB PRIMARY KEY NOT NULL REFERENCES media_files (id) ON DELETE CASCADE,
    format        TEXT NOT NULL CHECK (format IN ('epub', 'pdf', 'cbz')),
    -- images: pages served one at a time; reflowable (EPUB) and document (PDF): rendered by the
    -- client from the file.
    layout        TEXT NOT NULL CHECK (layout IN ('images', 'reflowable', 'document')),
    right_to_left INTEGER NOT NULL DEFAULT 0 CHECK (right_to_left IN (0, 1)),
    page_count    INTEGER NOT NULL DEFAULT 0,
    -- Size of each page (images layout), as JSON: [[width, height], ...].
    pages         TEXT NOT NULL DEFAULT '[]',
    -- Key drawn at random at analysis time, part of the URLs of the pages and the file: it stands
    -- in for authentication.
    access_key    TEXT NOT NULL
) STRICT;

-- Where a profile is in a book. "Read" and the last read date stay in user_data.
CREATE TABLE reading_progress (
    profile_id  BLOB NOT NULL REFERENCES profiles (id) ON DELETE CASCADE,
    item_id     BLOB NOT NULL REFERENCES items (id) ON DELETE CASCADE,
    -- Page (zero-based) for image and document layouts.
    page        INTEGER NOT NULL DEFAULT 0,
    -- Location given by the reader (EPUB: CFI), opaque to the server.
    locator     TEXT NOT NULL DEFAULT '',
    progression REAL NOT NULL DEFAULT 0 CHECK (progression BETWEEN 0 AND 1),
    updated_at  INTEGER NOT NULL,
    PRIMARY KEY (profile_id, item_id)
) STRICT;

CREATE INDEX reading_progress_recent ON reading_progress (profile_id, updated_at);

COMMIT;

PRAGMA foreign_keys = ON;

-- +goose Down
-- The widened constraints stay: books and photos, their tables and illustrators go away.
DROP TABLE reading_progress;

DROP TABLE book_files;

DROP TABLE books;

DELETE FROM libraries WHERE kind IN ('books', 'photos');

DELETE FROM item_people WHERE role = 'illustrator';
