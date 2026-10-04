-- Library order: the rank the administrator gives them (1, 2, 3...), followed by their lists and,
-- kind by kind, by the home rows. 0 means no rank: the library comes after the ranked ones, by kind
-- (movies, series, music, books, photos) then by name; that is the order of all of them as long as
-- nobody chose one.

-- +goose Up
ALTER TABLE libraries ADD COLUMN position INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE libraries DROP COLUMN position;
