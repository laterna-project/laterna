-- Catalog: per-profile user data and the search index.

-- +goose Up
-- What a profile did with an item. History belongs to the profile. For a series or a season only
-- "favorite" means anything: "played" is derived from the episodes.
CREATE TABLE user_data (
    profile_id     BLOB NOT NULL REFERENCES profiles (id) ON DELETE CASCADE,
    item_id        BLOB NOT NULL REFERENCES items (id) ON DELETE CASCADE,
    played         INTEGER NOT NULL DEFAULT 0 CHECK (played IN (0, 1)),
    -- Number of complete plays.
    play_count     INTEGER NOT NULL DEFAULT 0,
    -- Resume point; 0 means from the start.
    position_ms    INTEGER NOT NULL DEFAULT 0,
    last_played_at INTEGER,
    favorite       INTEGER NOT NULL DEFAULT 0 CHECK (favorite IN (0, 1)),
    updated_at     INTEGER NOT NULL,
    PRIMARY KEY (profile_id, item_id)
) STRICT;

CREATE INDEX user_data_item ON user_data (item_id);

-- Full-text search, ignoring accents and case ("amelie" finds "Amélie"), with prefix search.
-- External-content table: the text stays in `items` and triggers keep the index up to date. The
-- link goes through the implicit rowid of `items`: a VACUUM may renumber it, so it must be followed
-- by INSERT INTO items_fts (items_fts) VALUES ('rebuild').
CREATE VIRTUAL TABLE items_fts USING fts5 (
    title,
    original_title,
    content = 'items',
    content_rowid = 'rowid',
    tokenize = 'unicode61 remove_diacritics 2',
    prefix = '2 3'
);

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

-- Items already there.
INSERT INTO items_fts (items_fts) VALUES ('rebuild');

-- +goose Down
DROP TRIGGER items_fts_update;
DROP TRIGGER items_fts_delete;
DROP TRIGGER items_fts_insert;
DROP TABLE items_fts;
DROP TABLE user_data;
