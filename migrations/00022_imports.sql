-- Import from Jellyfin: an imported play keeps the key of its source, so that a second import does
-- not copy it again.

-- +goose Up
ALTER TABLE play_history ADD COLUMN import_key TEXT;

CREATE UNIQUE INDEX play_history_import ON play_history (profile_id, import_key) WHERE import_key IS NOT NULL;

-- +goose Down
DROP INDEX play_history_import;
ALTER TABLE play_history DROP COLUMN import_key;
