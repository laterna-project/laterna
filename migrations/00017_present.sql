-- Performance budgets: an item knows whether it has a file present. Lists no longer check, item by
-- item, that one of its files is there (two lookups per row): they walk an index in the wanted
-- order and stop at the end of the page.
--
-- The column is maintained by triggers, so no code path can forget it. Only items that have files
-- (movies, episodes, tracks, books, photos) set it; series, seasons, artists, albums and book
-- series derive it from their children.

-- +goose Up
ALTER TABLE items ADD COLUMN present INTEGER NOT NULL DEFAULT 0 CHECK (present IN (0, 1));

UPDATE items SET present = EXISTS (
    SELECT 1 FROM item_files l JOIN media_files m ON m.id = l.file_id
    WHERE l.item_id = items.id AND m.missing_since IS NULL
);

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

-- A file linked to another item (LinkFile): both need another look.
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

-- Pages by title or by date added, for all libraries or one: the index gives the whole order (ID
-- included, which breaks ties).
DROP INDEX items_library_kind;

DROP INDEX items_added;

CREATE INDEX items_kind_title ON items (kind, present, sort_title, id);

CREATE INDEX items_kind_added ON items (kind, present, added_at, id);

CREATE INDEX items_library_title ON items (library_id, kind, present, sort_title, id);

CREATE INDEX items_library_added ON items (library_id, kind, present, added_at, id);

-- (Present) children of an item: replaces the index on parent_id alone.
DROP INDEX items_parent;

CREATE INDEX items_parent_present ON items (parent_id, present);

-- +goose Down
DROP INDEX items_parent_present;

CREATE INDEX items_parent ON items (parent_id);

DROP INDEX items_library_added;

DROP INDEX items_library_title;

DROP INDEX items_kind_added;

DROP INDEX items_kind_title;

CREATE INDEX items_library_kind ON items (library_id, kind, sort_title);

CREATE INDEX items_added ON items (library_id, kind, added_at);

DROP TRIGGER media_files_present;

DROP TRIGGER item_files_present_update;

DROP TRIGGER item_files_present_delete;

DROP TRIGGER item_files_present_insert;

ALTER TABLE items DROP COLUMN present;
