-- Offline downloads: files a device asked for to play without a network, as they are or prepared by
-- the server (conversion to MP4, AAC).

-- +goose Up
CREATE TABLE downloads (
    id         BLOB PRIMARY KEY NOT NULL,
    account_id BLOB NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    profile_id BLOB NOT NULL REFERENCES profiles (id) ON DELETE CASCADE,
    -- The device that asked for it: a download goes away with its session.
    session_id BLOB NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    item_id    BLOB NOT NULL REFERENCES items (id) ON DELETE CASCADE,
    file_id    BLOB NOT NULL REFERENCES media_files (id) ON DELETE CASCADE,
    quality    TEXT NOT NULL CHECK (quality IN ('original', 'high', 'medium', 'low')),
    state      TEXT NOT NULL CHECK (state IN ('queued', 'preparing', 'ready', 'failed')),
    -- Preparation decision (streams copied or re-encoded, bitrates), as JSON.
    plan       TEXT NOT NULL,
    -- Progress of the preparation, from 0 to 1.
    progress   REAL NOT NULL DEFAULT 0,
    -- Expected and real sizes, in bytes.
    estimate   INTEGER NOT NULL DEFAULT 0,
    size       INTEGER NOT NULL DEFAULT 0,
    -- File served (the original, or the prepared copy in the cache); empty until ready.
    path       TEXT NOT NULL DEFAULT '',
    error      TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    ready_at   INTEGER
) STRICT;

CREATE INDEX downloads_session ON downloads (session_id, profile_id, created_at);

CREATE INDEX downloads_ready ON downloads (state, ready_at);

-- Offline plays already applied (profile, item, time of the playback): a report replayed by the
-- device does not count twice. Forgotten after 90 days.
CREATE TABLE offline_plays (
    profile_id BLOB NOT NULL REFERENCES profiles (id) ON DELETE CASCADE,
    item_id    BLOB NOT NULL REFERENCES items (id) ON DELETE CASCADE,
    played_at  INTEGER NOT NULL,
    PRIMARY KEY (profile_id, item_id, played_at)
) STRICT;

-- Downloads taken away from an account by an administrator (never from an administrator).
ALTER TABLE accounts ADD COLUMN deny_downloads INTEGER NOT NULL DEFAULT 0 CHECK (deny_downloads IN (0, 1));

-- +goose Down
ALTER TABLE accounts DROP COLUMN deny_downloads;

DROP TABLE offline_plays;

DROP TABLE downloads;
