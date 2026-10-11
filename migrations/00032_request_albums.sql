-- Albums of an artist chosen once Lidarr lists them (docs/design/requests.md): between its approval
-- and its search, the request waits for the choice. That wait is a column next to the status
-- 'approved', not a status of its own: the statuses are in a CHECK constraint, which SQLite cannot
-- alter without rebuilding the table.

-- +goose Up
ALTER TABLE requests ADD COLUMN choosing INTEGER NOT NULL DEFAULT 0 CHECK (choosing IN (0, 1));

-- The albums chosen, as a JSON array of titles.
ALTER TABLE requests ADD COLUMN album_titles TEXT NOT NULL DEFAULT '[]';

-- +goose Down
ALTER TABLE requests DROP COLUMN album_titles;

ALTER TABLE requests DROP COLUMN choosing;
