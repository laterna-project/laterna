-- Web push (docs/design/notifications.md): where to reach the devices that asked to be told when
-- the app is closed.

-- +goose Up
CREATE TABLE push_subscriptions (
    -- One subscription per device. It follows the profile picked on that device, and goes away
    -- with the session.
    session_id BLOB PRIMARY KEY NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    -- Address of the push service for the device, then the key and the secret to encrypt for it
    -- (base64url, as the browser gives them).
    endpoint   TEXT NOT NULL UNIQUE,
    p256dh     TEXT NOT NULL,
    auth       TEXT NOT NULL,
    created_at INTEGER NOT NULL
) STRICT;

-- +goose Down
DROP TABLE push_subscriptions;
