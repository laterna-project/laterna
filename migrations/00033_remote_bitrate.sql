-- Highest bitrate of an account's playbacks from outside the local network, in kbit/s
-- (docs/design/playback.md); 0 means only the server's limit.

-- +goose Up
ALTER TABLE accounts ADD COLUMN remote_max_bitrate INTEGER NOT NULL DEFAULT 0 CHECK (remote_max_bitrate >= 0);

-- +goose Down
ALTER TABLE accounts DROP COLUMN remote_max_bitrate;
