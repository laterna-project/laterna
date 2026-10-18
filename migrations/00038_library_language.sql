-- Libraries no longer have a language (contract 1.0): metadata come from the NFO files only, in
-- whatever language they are written.

-- +goose Up
ALTER TABLE libraries DROP COLUMN language;

-- +goose Down
ALTER TABLE libraries ADD COLUMN language TEXT NOT NULL DEFAULT 'en-US';
