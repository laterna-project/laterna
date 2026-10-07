-- Episode stills (internal/app/stills.go): an episode without a thumbnail gets a frame of its video
-- when its metadata is read. The episodes already in the library without one have their metadata
-- read again once, in the background, after any other waiting work of the class. Kind, class and
-- priority are those of the item.metadata job (internal/app/background.go); the target is the
-- item's ID in its canonical form.

-- +goose Up
INSERT INTO jobs (kind, target, class, priority, state, attempts, run_after, created_at, updated_at)
SELECT 'item.metadata',
       lower(substr(hex(i.id), 1, 8) || '-' || substr(hex(i.id), 9, 4) || '-' || substr(hex(i.id), 13, 4) || '-' ||
             substr(hex(i.id), 17, 4) || '-' || substr(hex(i.id), 21, 12)),
       'io', -1, 'pending', 0,
       CAST(unixepoch('subsec') * 1000 AS INTEGER), CAST(unixepoch('subsec') * 1000 AS INTEGER),
       CAST(unixepoch('subsec') * 1000 AS INTEGER)
FROM items i
WHERE i.kind = 'episode'
  AND NOT EXISTS (SELECT 1 FROM images m WHERE m.item_id = i.id AND m.kind = 'thumb')
ON CONFLICT (kind, target) WHERE state = 'pending' DO NOTHING;

-- +goose Down
-- Nothing to undo: the jobs run or are dropped with the database.
SELECT 1;
