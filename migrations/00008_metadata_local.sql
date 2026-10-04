-- Local metadata only: NFO files and images written by Sonarr, Radarr or another tool; no online
-- provider anymore.

-- +goose Up
-- Signature of the metadata files (NFO, images) of each folder of a library: names, sizes and
-- dates. The scan compares them and reads the metadata of the items of a folder again when its
-- signature changed.
CREATE TABLE metadata_dirs (
    library_id BLOB NOT NULL REFERENCES libraries (id) ON DELETE CASCADE,
    path       TEXT NOT NULL,
    signature  TEXT NOT NULL,
    PRIMARY KEY (library_id, path)
) STRICT;

-- Everything is read again once: titles, overviews and images that came from online providers give
-- way to file names and NFO files.
INSERT INTO jobs (kind, target, class, priority, state, attempts, run_after, created_at, updated_at)
SELECT 'item.metadata',
       lower(substr(hex(id), 1, 8) || '-' || substr(hex(id), 9, 4) || '-' || substr(hex(id), 13, 4) || '-' ||
             substr(hex(id), 17, 4) || '-' || substr(hex(id), 21, 12)),
       'io', 0, 'pending', 0,
       CAST(unixepoch('subsec') * 1000 AS INTEGER), CAST(unixepoch('subsec') * 1000 AS INTEGER),
       CAST(unixepoch('subsec') * 1000 AS INTEGER)
FROM items
WHERE true
ON CONFLICT (kind, target) WHERE state = 'pending' DO NOTHING;

-- +goose Down
DROP TABLE metadata_dirs;
