-- Notifications (docs/design/notifications.md): what a profile is told about, kept until it deletes
-- them or they get old.

-- +goose Up
CREATE TABLE notifications (
    id         BLOB PRIMARY KEY NOT NULL,
    profile_id BLOB NOT NULL REFERENCES profiles (id) ON DELETE CASCADE,
    -- No CHECK on the kind: new kinds come without rebuilding the table.
    kind       TEXT NOT NULL,
    -- What it says, as composed text (JSON).
    text       TEXT NOT NULL,
    -- What it is about, while that exists.
    item_id    BLOB REFERENCES items (id) ON DELETE SET NULL,
    request_id BLOB REFERENCES requests (id) ON DELETE SET NULL,
    created_at INTEGER NOT NULL,
    read_at    INTEGER
) STRICT;

-- IDs are UUIDv7: their order is the order of creation.
CREATE INDEX notifications_profile ON notifications (profile_id, id);

CREATE INDEX notifications_created ON notifications (created_at);

-- +goose Down
DROP TABLE notifications;
