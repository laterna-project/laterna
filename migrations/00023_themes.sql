-- Themes: design tokens (dark and light palettes, radius, density, font) that each client maps to
-- its own UI; never CSS. Built-in themes are written at startup, others are created by an
-- administrator; each profile picks its own.
--
-- The logo and background of a theme are images like any other (image route, resized versions,
-- blurhash): the images table gets a third owner, the theme, and the "upload" source. SQLite cannot
-- alter a CHECK constraint, so the table is rebuilt as in 00013, with foreign keys off, outside a
-- goose transaction.

-- +goose NO TRANSACTION
-- +goose Up
PRAGMA foreign_keys = OFF;

-- Safety net: if foreign keys were still on, the migration stops here, before anything is dropped.
CREATE TEMP TABLE themes_fk_guard (active INTEGER NOT NULL CHECK (active = 0));

INSERT INTO themes_fk_guard SELECT foreign_keys FROM pragma_foreign_keys;

DROP TABLE themes_fk_guard;

BEGIN;

CREATE TABLE themes (
    id         BLOB PRIMARY KEY NOT NULL,
    name       TEXT NOT NULL,
    name_key   TEXT NOT NULL UNIQUE,
    built_in   INTEGER NOT NULL DEFAULT 0 CHECK (built_in IN (0, 1)),
    -- Tokens of the theme, as JSON (domain.ThemeTokens).
    tokens     TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;

-- Theme picked by the profile (NULL means the server's) and mode (auto follows the device).
ALTER TABLE profiles ADD COLUMN theme_id BLOB REFERENCES themes (id) ON DELETE SET NULL;
ALTER TABLE profiles ADD COLUMN theme_mode TEXT NOT NULL DEFAULT 'auto' CHECK (theme_mode IN ('auto', 'dark', 'light'));

CREATE TABLE images_new (
    id         BLOB PRIMARY KEY NOT NULL,
    item_id    BLOB REFERENCES items (id) ON DELETE CASCADE,
    person_id  BLOB REFERENCES people (id) ON DELETE CASCADE,
    theme_id   BLOB REFERENCES themes (id) ON DELETE CASCADE,
    kind       TEXT NOT NULL,
    source     TEXT NOT NULL CHECK (source IN ('local', 'remote', 'embedded', 'upload')),
    path       TEXT NOT NULL,
    remote_url TEXT NOT NULL DEFAULT '',
    width      INTEGER NOT NULL DEFAULT 0,
    height     INTEGER NOT NULL DEFAULT 0,
    blurhash   TEXT NOT NULL DEFAULT '',
    -- Content hash, empty until the image is analyzed.
    hash       TEXT NOT NULL DEFAULT '',
    updated_at INTEGER NOT NULL,
    CHECK ((item_id IS NOT NULL) + (person_id IS NOT NULL) + (theme_id IS NOT NULL) = 1),
    UNIQUE (item_id, kind),
    UNIQUE (person_id, kind),
    UNIQUE (theme_id, kind)
) STRICT;

INSERT INTO images_new (id, item_id, person_id, kind, source, path, remote_url, width, height, blurhash, hash, updated_at)
SELECT id, item_id, person_id, kind, source, path, remote_url, width, height, blurhash, hash, updated_at FROM images;

DROP TABLE images;

ALTER TABLE images_new RENAME TO images;

COMMIT;

PRAGMA foreign_keys = ON;

-- +goose Down
PRAGMA foreign_keys = OFF;

CREATE TEMP TABLE themes_fk_guard (active INTEGER NOT NULL CHECK (active = 0));

INSERT INTO themes_fk_guard SELECT foreign_keys FROM pragma_foreign_keys;

DROP TABLE themes_fk_guard;

BEGIN;

CREATE TABLE images_old (
    id         BLOB PRIMARY KEY NOT NULL,
    item_id    BLOB REFERENCES items (id) ON DELETE CASCADE,
    person_id  BLOB REFERENCES people (id) ON DELETE CASCADE,
    kind       TEXT NOT NULL,
    source     TEXT NOT NULL CHECK (source IN ('local', 'remote', 'embedded')),
    path       TEXT NOT NULL,
    remote_url TEXT NOT NULL DEFAULT '',
    width      INTEGER NOT NULL DEFAULT 0,
    height     INTEGER NOT NULL DEFAULT 0,
    blurhash   TEXT NOT NULL DEFAULT '',
    -- Content hash, empty until the image is analyzed.
    hash       TEXT NOT NULL DEFAULT '',
    updated_at INTEGER NOT NULL,
    CHECK ((item_id IS NULL) <> (person_id IS NULL)),
    UNIQUE (item_id, kind),
    UNIQUE (person_id, kind)
) STRICT;

INSERT INTO images_old (id, item_id, person_id, kind, source, path, remote_url, width, height, blurhash, hash, updated_at)
SELECT id, item_id, person_id, kind, source, path, remote_url, width, height, blurhash, hash, updated_at FROM images
WHERE theme_id IS NULL;

DROP TABLE images;

ALTER TABLE images_old RENAME TO images;

ALTER TABLE profiles DROP COLUMN theme_mode;

ALTER TABLE profiles DROP COLUMN theme_id;

DROP TABLE themes;

COMMIT;

PRAGMA foreign_keys = ON;
