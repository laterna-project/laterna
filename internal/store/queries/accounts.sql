-- name: CountAccounts :one
SELECT count(*) FROM accounts;

-- name: InsertAccount :exec
INSERT INTO accounts (id, username, username_key, password_hash, is_admin, disabled, all_libraries, max_age,
                      block_unrated, deny_downloads, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, 0, ?, ?, ?, ?, ?, ?);

-- name: UpdateAccount :exec
UPDATE accounts
SET username = ?, username_key = ?, is_admin = ?, disabled = ?, all_libraries = ?, max_age = ?, block_unrated = ?,
    deny_downloads = ?, updated_at = ?
WHERE id = ?;

-- name: DeleteAccount :exec
DELETE FROM accounts WHERE id = ?;

-- name: ListAccountSummaries :many
SELECT a.*,
       (SELECT count(*) FROM profiles p WHERE p.account_id = a.id) AS profile_count,
       CAST(coalesce((SELECT max(s.last_used_at) FROM sessions s WHERE s.account_id = a.id), 0) AS INTEGER) AS last_active
FROM accounts a
ORDER BY a.created_at, a.id;

-- name: CountEnabledAdmins :one
SELECT count(*) FROM accounts WHERE is_admin = 1 AND disabled = 0;

-- name: ListAccountLibraries :many
SELECT library_id FROM account_libraries WHERE account_id = ?;

-- name: ListAllAccountLibraries :many
SELECT account_id, library_id FROM account_libraries;

-- name: DeleteAccountLibraries :exec
DELETE FROM account_libraries WHERE account_id = ?;

-- name: InsertAccountLibrary :exec
INSERT INTO account_libraries (account_id, library_id) VALUES (?, ?) ON CONFLICT DO NOTHING;

-- name: GetAccount :one
SELECT * FROM accounts WHERE id = ?;

-- name: GetAccountByUsername :one
SELECT * FROM accounts WHERE username_key = ?;

-- name: UpdateAccountPassword :exec
UPDATE accounts SET password_hash = ?, updated_at = ? WHERE id = ?;

-- name: InsertProfile :exec
INSERT INTO profiles (id, account_id, name, name_key, pin_hash, kid, max_age, block_unrated, language, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetProfile :one
SELECT * FROM profiles WHERE id = ?;

-- name: ListProfiles :many
SELECT * FROM profiles WHERE account_id = ? ORDER BY created_at, id;

-- name: CountProfiles :one
SELECT count(*) FROM profiles WHERE account_id = ?;

-- name: UpdateProfile :exec
UPDATE profiles
SET name = ?, name_key = ?, pin_hash = ?, kid = ?, max_age = ?, block_unrated = ?, language = ?,
    subtitle_mode = ?, subtitle_language = ?, updated_at = ?
WHERE id = ?;

-- name: DeleteProfile :exec
DELETE FROM profiles WHERE id = ?;

-- name: InsertSession :exec
INSERT INTO sessions (id, account_id, profile_id, token_hash, device_name, client, client_version, platform,
                      created_at, last_used_at, expires_at, last_ip)
VALUES (?, ?, NULL, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetSessionByTokenHash :one
SELECT * FROM sessions WHERE token_hash = ?;

-- name: GetSession :one
SELECT * FROM sessions WHERE id = ?;

-- name: ListSessions :many
SELECT * FROM sessions WHERE account_id = ? AND expires_at > ? ORDER BY last_used_at DESC, id;

-- name: TouchSession :exec
UPDATE sessions SET last_used_at = ?, expires_at = ?, last_ip = ? WHERE id = ?;

-- name: SetSessionProfile :exec
UPDATE sessions SET profile_id = ? WHERE id = ?;

-- name: DeleteSession :exec
DELETE FROM sessions WHERE id = ?;

-- name: DeleteAccountSessions :exec
DELETE FROM sessions WHERE account_id = ?;

-- name: DeleteOtherSessions :exec
DELETE FROM sessions WHERE account_id = ? AND id <> ?;

-- name: DeleteExpiredSessions :execrows
DELETE FROM sessions WHERE expires_at <= ?;
