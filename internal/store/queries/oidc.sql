-- name: GetOidcAccount :one
SELECT account_id FROM oidc_links WHERE issuer = ? AND subject = ?;

-- name: InsertOidcLink :exec
INSERT INTO oidc_links (issuer, subject, account_id, created_at) VALUES (?, ?, ?, ?);
