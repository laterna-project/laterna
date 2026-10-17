-- Where the lyrics of a track are (docs/design/media-types.md): a .lrc file next to it, or its
-- tags. The lyrics themselves are read when asked for.

-- +goose Up
ALTER TABLE tracks ADD COLUMN lyrics TEXT NOT NULL DEFAULT '' CHECK (lyrics IN ('', 'file', 'tags'));

-- +goose Down
ALTER TABLE tracks DROP COLUMN lyrics;
