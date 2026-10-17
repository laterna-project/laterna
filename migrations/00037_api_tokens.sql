-- API tokens (docs/design/accounts.md): sessions of a tool rather than of a device, named after
-- what they are for, pinned to the profile that made them, possibly read-only.

-- +goose Up
ALTER TABLE sessions ADD COLUMN kind TEXT NOT NULL DEFAULT 'device' CHECK (kind IN ('device', 'api'));
ALTER TABLE sessions ADD COLUMN read_only INTEGER NOT NULL DEFAULT 0 CHECK (read_only IN (0, 1));

-- +goose Down
DELETE FROM sessions WHERE kind = 'api';

ALTER TABLE sessions DROP COLUMN read_only;

ALTER TABLE sessions DROP COLUMN kind;
