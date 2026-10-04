-- Server settings that can change at runtime (identity, branding...), as key/value pairs.
-- Structured values are stored as JSON and typed on the Go side.

-- +goose Up
CREATE TABLE settings (
    key   TEXT PRIMARY KEY NOT NULL,
    value TEXT NOT NULL
) STRICT;

-- +goose Down
DROP TABLE settings;
