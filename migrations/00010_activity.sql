-- Activity log: logins, accounts, libraries, playbacks, settings, integrations, failed jobs. Kept
-- for 90 days.

-- +goose Up
CREATE TABLE activity (
    id         INTEGER PRIMARY KEY,
    at         INTEGER NOT NULL,
    kind       TEXT NOT NULL,
    warning    INTEGER NOT NULL DEFAULT 0 CHECK (warning IN (0, 1)),
    -- Who, and on what: cleared with the account, the profile or the item; the sentence keeps the
    -- names.
    account_id BLOB REFERENCES accounts (id) ON DELETE SET NULL,
    profile_id BLOB REFERENCES profiles (id) ON DELETE SET NULL,
    item_id    BLOB REFERENCES items (id) ON DELETE SET NULL,
    summary    TEXT NOT NULL
) STRICT;

CREATE INDEX activity_at ON activity (at);

-- +goose Down
DROP TABLE activity;
