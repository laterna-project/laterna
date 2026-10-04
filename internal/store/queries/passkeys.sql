-- name: InsertPasskey :exec
INSERT INTO passkeys (id, account_id, credential_id, public_key, sign_count, name, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: GetPasskeyByCredential :one
SELECT * FROM passkeys WHERE credential_id = ?;

-- name: ListAccountPasskeys :many
SELECT * FROM passkeys WHERE account_id = ? ORDER BY created_at, id;

-- name: TouchPasskey :exec
UPDATE passkeys SET sign_count = ?, last_used_at = ? WHERE id = ?;

-- name: DeletePasskey :execrows
DELETE FROM passkeys WHERE id = ? AND account_id = ?;
