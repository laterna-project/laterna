-- Accounts, profiles and sessions. Conventions: IDs are BLOB(16) (UUIDv7), dates are Unix
-- milliseconds (INTEGER), booleans are INTEGER 0/1, hashes and hashed secrets are TEXT.

-- +goose Up
CREATE TABLE accounts (
    id            BLOB PRIMARY KEY NOT NULL,
    username      TEXT NOT NULL,
    -- Normalized name (lower case): uniqueness ignores case.
    username_key  TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    is_admin      INTEGER NOT NULL DEFAULT 0 CHECK (is_admin IN (0, 1)),
    disabled      INTEGER NOT NULL DEFAULT 0 CHECK (disabled IN (0, 1)),
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL
) STRICT;

CREATE TABLE profiles (
    id         BLOB PRIMARY KEY NOT NULL,
    account_id BLOB NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    name_key   TEXT NOT NULL,
    -- argon2id hash of the PIN; NULL means no PIN.
    pin_hash   TEXT,
    kid        INTEGER NOT NULL DEFAULT 0 CHECK (kid IN (0, 1)),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    UNIQUE (account_id, name_key)
) STRICT;

CREATE TABLE sessions (
    id             BLOB PRIMARY KEY NOT NULL,
    account_id     BLOB NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    profile_id     BLOB REFERENCES profiles (id) ON DELETE SET NULL,
    -- SHA-256 of the token: the token itself is never stored.
    token_hash     TEXT NOT NULL UNIQUE,
    device_name    TEXT NOT NULL,
    client         TEXT NOT NULL,
    client_version TEXT NOT NULL,
    platform       TEXT NOT NULL,
    created_at     INTEGER NOT NULL,
    last_used_at   INTEGER NOT NULL,
    expires_at     INTEGER NOT NULL,
    last_ip        TEXT NOT NULL DEFAULT ''
) STRICT;

CREATE INDEX sessions_account ON sessions (account_id);

CREATE INDEX sessions_expires ON sessions (expires_at);

-- +goose Down
DROP TABLE sessions;
DROP TABLE profiles;
DROP TABLE accounts;
