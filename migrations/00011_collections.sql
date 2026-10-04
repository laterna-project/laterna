-- Collections and playlists.

-- +goose Up
-- Collections shared by the whole server: read from NFO files (nfo_key: "tmdb:<id>" or
-- "name:<normalized name>") or created by an administrator (empty nfo_key).
CREATE TABLE collections (
    id         BLOB PRIMARY KEY NOT NULL,
    name       TEXT NOT NULL,
    overview   TEXT NOT NULL DEFAULT '',
    nfo_key    TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;

CREATE UNIQUE INDEX collections_nfo ON collections (nfo_key) WHERE nfo_key <> '';

CREATE TABLE collection_items (
    collection_id BLOB NOT NULL REFERENCES collections (id) ON DELETE CASCADE,
    item_id       BLOB NOT NULL REFERENCES items (id) ON DELETE CASCADE,
    added_at      INTEGER NOT NULL,
    PRIMARY KEY (collection_id, item_id)
) STRICT;

CREATE INDEX collection_items_item ON collection_items (item_id);

-- Playlists of a profile: ordered entries (position from 0 to n-1), where the same item may appear
-- more than once.
CREATE TABLE playlists (
    id         BLOB PRIMARY KEY NOT NULL,
    profile_id BLOB NOT NULL REFERENCES profiles (id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;

CREATE INDEX playlists_profile ON playlists (profile_id);

CREATE TABLE playlist_entries (
    id          BLOB PRIMARY KEY NOT NULL,
    playlist_id BLOB NOT NULL REFERENCES playlists (id) ON DELETE CASCADE,
    item_id     BLOB NOT NULL REFERENCES items (id) ON DELETE CASCADE,
    position    INTEGER NOT NULL,
    added_at    INTEGER NOT NULL
) STRICT;

CREATE INDEX playlist_entries_order ON playlist_entries (playlist_id, position);

-- Movies are read again once, to file those whose NFO names a collection.
INSERT INTO jobs (kind, target, class, priority, state, attempts, run_after, created_at, updated_at)
SELECT 'item.metadata',
       lower(substr(hex(id), 1, 8) || '-' || substr(hex(id), 9, 4) || '-' || substr(hex(id), 13, 4) || '-' ||
             substr(hex(id), 17, 4) || '-' || substr(hex(id), 21, 12)),
       'io', 0, 'pending', 0,
       CAST(unixepoch('subsec') * 1000 AS INTEGER), CAST(unixepoch('subsec') * 1000 AS INTEGER),
       CAST(unixepoch('subsec') * 1000 AS INTEGER)
FROM items
WHERE kind IN ('movie', 'series')
ON CONFLICT (kind, target) WHERE state = 'pending' DO NOTHING;

-- +goose Down
DROP TABLE playlist_entries;
DROP TABLE playlists;
DROP TABLE collection_items;
DROP TABLE collections;
