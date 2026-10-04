-- name: UpsertBuiltinTheme :exec
INSERT INTO themes (id, name, name_key, built_in, tokens, created_at, updated_at) VALUES (?, ?, ?, 1, ?, ?, ?)
ON CONFLICT (id) DO UPDATE SET name = excluded.name, name_key = excluded.name_key, built_in = 1, tokens = excluded.tokens,
    updated_at = excluded.updated_at
WHERE themes.tokens <> excluded.tokens OR themes.name <> excluded.name;

-- name: InsertTheme :exec
INSERT INTO themes (id, name, name_key, built_in, tokens, created_at, updated_at) VALUES (?, ?, ?, 0, ?, ?, ?);

-- name: UpdateTheme :exec
UPDATE themes SET name = ?, name_key = ?, tokens = ?, updated_at = ? WHERE id = ? AND built_in = 0;

-- name: DeleteTheme :execrows
DELETE FROM themes WHERE id = ? AND built_in = 0;

-- name: GetTheme :one
SELECT * FROM themes WHERE id = ?;

-- name: ListThemes :many
SELECT * FROM themes ORDER BY built_in DESC, name_key;

-- name: ListThemeImages :many
SELECT * FROM images WHERE theme_id IS NOT NULL;

-- name: GetThemeImage :one
SELECT * FROM images WHERE theme_id = ? AND kind = ?;

-- name: InsertThemeImage :exec
INSERT INTO images (id, theme_id, kind, source, path, width, height, blurhash, hash, updated_at)
VALUES (?, ?, ?, 'upload', ?, ?, ?, ?, ?, ?);

-- name: SetProfileTheme :exec
UPDATE profiles SET theme_id = ?, theme_mode = ?, updated_at = ? WHERE id = ?;
