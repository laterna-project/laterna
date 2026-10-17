-- Extras of movies and series (trailers, featurettes...; docs/design/library.md): a kind of item,
-- whose parent is its movie or series, and what is specific to it.
--
-- As for books (00015): items is rebuilt to widen its kinds, with foreign keys off during the
-- rebuild, outside a goose transaction. Its columns, indexes and triggers are those of 00017. The
-- triggers of other tables that write to items are dropped first and made again after: renaming
-- the new table checks every trigger, and they would name a table that is gone.

-- +goose NO TRANSACTION
-- +goose Up
PRAGMA foreign_keys = OFF;

-- Safety net: if foreign keys were still on, the migration stops here, before anything is dropped.
CREATE TEMP TABLE extras_fk_guard (active INTEGER NOT NULL CHECK (active = 0));

INSERT INTO extras_fk_guard SELECT foreign_keys FROM pragma_foreign_keys;

DROP TABLE extras_fk_guard;

BEGIN;

DROP TRIGGER item_files_present_insert;

DROP TRIGGER item_files_present_delete;

DROP TRIGGER item_files_present_update;

DROP TRIGGER media_files_present;

CREATE TABLE items_new (
    id               BLOB PRIMARY KEY NOT NULL,
    library_id       BLOB NOT NULL REFERENCES libraries (id) ON DELETE CASCADE,
    kind             TEXT NOT NULL CHECK (kind IN ('movie', 'series', 'season', 'episode', 'artist', 'album', 'track',
                                                   'book_series', 'book', 'photo_album', 'photo', 'extra')),
    -- Season -> series, episode -> season, album -> artist, track -> album, book -> book series,
    -- photo -> photo album, photo album -> the album that contains it, extra -> its movie or series.
    parent_id        BLOB REFERENCES items (id) ON DELETE CASCADE,
    -- Groups the files of one item: folder + title (movie), folder (series), series + number
    -- (season, episode), name (artist), artist + title (album), album + disc + number (track), name
    -- (book series), series + volume or title (book), folder (photo album), file (photo, extra).
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
    -- A file of the item is present (kept by the triggers of 00017).
    present          INTEGER NOT NULL DEFAULT 0 CHECK (present IN (0, 1)),
    UNIQUE (library_id, group_key)
) STRICT;

-- The rowid is kept: the search index (items_fts) refers to it.
INSERT INTO items_new (rowid, id, library_id, kind, parent_id, group_key, title, sort_title, original_title, year,
                       premiere_date, overview, tagline, official_rating, community_rating, runtime_ms, metadata_at,
                       added_at, updated_at, age_rating, present)
SELECT rowid, id, library_id, kind, parent_id, group_key, title, sort_title, original_title, year,
       premiere_date, overview, tagline, official_rating, community_rating, runtime_ms, metadata_at,
       added_at, updated_at, age_rating, present
FROM items;

DROP TABLE items;

ALTER TABLE items_new RENAME TO items;

CREATE INDEX items_parent_present ON items (parent_id, present);

CREATE INDEX items_kind_title ON items (kind, present, sort_title, id);

CREATE INDEX items_kind_added ON items (kind, present, added_at, id);

CREATE INDEX items_library_title ON items (library_id, kind, present, sort_title, id);

CREATE INDEX items_library_added ON items (library_id, kind, present, added_at, id);

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

-- +goose StatementBegin
CREATE TRIGGER item_files_present_insert AFTER INSERT ON item_files BEGIN
    UPDATE items SET present = EXISTS (
        SELECT 1 FROM item_files l JOIN media_files m ON m.id = l.file_id
        WHERE l.item_id = new.item_id AND m.missing_since IS NULL
    ) WHERE id = new.item_id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER item_files_present_delete AFTER DELETE ON item_files BEGIN
    UPDATE items SET present = EXISTS (
        SELECT 1 FROM item_files l JOIN media_files m ON m.id = l.file_id
        WHERE l.item_id = old.item_id AND m.missing_since IS NULL
    ) WHERE id = old.item_id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER item_files_present_update AFTER UPDATE OF item_id ON item_files BEGIN
    UPDATE items SET present = EXISTS (
        SELECT 1 FROM item_files l JOIN media_files m ON m.id = l.file_id
        WHERE l.item_id = items.id AND m.missing_since IS NULL
    ) WHERE id IN (old.item_id, new.item_id);
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER media_files_present AFTER UPDATE OF missing_since ON media_files BEGIN
    UPDATE items SET present = EXISTS (
        SELECT 1 FROM item_files l JOIN media_files m ON m.id = l.file_id
        WHERE l.item_id = items.id AND m.missing_since IS NULL
    ) WHERE id IN (SELECT item_id FROM item_files WHERE file_id = new.id);
END;
-- +goose StatementEnd

-- What is specific to an extra; its movie or series is its parent.
CREATE TABLE extras (
    item_id BLOB PRIMARY KEY NOT NULL REFERENCES items (id) ON DELETE CASCADE,
    kind    TEXT NOT NULL CHECK (kind IN ('trailer', 'featurette', 'behind_the_scenes', 'deleted_scene', 'interview',
                                          'scene', 'short', 'other'))
) STRICT;

COMMIT;

PRAGMA foreign_keys = ON;

-- +goose Down
-- The widened constraint stays: the extras and their table go away.
DELETE FROM items WHERE kind = 'extra';

DROP TABLE extras;
