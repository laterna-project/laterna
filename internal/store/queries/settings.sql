-- name: GetSetting :one
SELECT value FROM settings WHERE key = ?;

-- name: ListSettings :many
SELECT key, value FROM settings ORDER BY key;

-- name: UpsertSetting :exec
INSERT INTO settings (key, value) VALUES (?, ?)
ON CONFLICT (key) DO UPDATE SET value = excluded.value;

-- name: InsertSettingIfAbsent :exec
INSERT INTO settings (key, value) VALUES (?, ?)
ON CONFLICT (key) DO NOTHING;

-- name: DeleteSetting :exec
DELETE FROM settings WHERE key = ?;
