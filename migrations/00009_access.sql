-- Accounts managed by the administrator, per-library access and parental control.

-- +goose Up
-- Libraries of an account: all of them (all_libraries = 1, future ones included), or those in
-- account_libraries.
ALTER TABLE accounts ADD COLUMN all_libraries INTEGER NOT NULL DEFAULT 1 CHECK (all_libraries IN (0, 1));

CREATE TABLE account_libraries (
    account_id BLOB NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    library_id BLOB NOT NULL REFERENCES libraries (id) ON DELETE CASCADE,
    PRIMARY KEY (account_id, library_id)
) STRICT;

-- Parental control: maximum age (NULL means no limit) and unrated content hidden, on the account
-- (set by an administrator) and on the profile (set by the account holder); the stricter wins.
ALTER TABLE accounts ADD COLUMN max_age INTEGER CHECK (max_age BETWEEN 0 AND 21);
ALTER TABLE accounts ADD COLUMN block_unrated INTEGER NOT NULL DEFAULT 0 CHECK (block_unrated IN (0, 1));
ALTER TABLE profiles ADD COLUMN max_age INTEGER CHECK (max_age BETWEEN 0 AND 21);
ALTER TABLE profiles ADD COLUMN block_unrated INTEGER NOT NULL DEFAULT 0 CHECK (block_unrated IN (0, 1));

-- Existing kid profiles get the default control of a kid profile.
UPDATE profiles SET max_age = 10, block_unrated = 1 WHERE kid = 1;

-- Age derived from the rating (official_rating); NULL means unrated. An episode or a season without
-- a rating takes that of its series, at read time.
ALTER TABLE items ADD COLUMN age_rating INTEGER CHECK (age_rating BETWEEN 0 AND 21);

-- Metadata is read again once to compute the age of the ratings already known.
INSERT INTO jobs (kind, target, class, priority, state, attempts, run_after, created_at, updated_at)
SELECT 'item.metadata',
       lower(substr(hex(id), 1, 8) || '-' || substr(hex(id), 9, 4) || '-' || substr(hex(id), 13, 4) || '-' ||
             substr(hex(id), 17, 4) || '-' || substr(hex(id), 21, 12)),
       'io', 0, 'pending', 0,
       CAST(unixepoch('subsec') * 1000 AS INTEGER), CAST(unixepoch('subsec') * 1000 AS INTEGER),
       CAST(unixepoch('subsec') * 1000 AS INTEGER)
FROM items
WHERE official_rating <> ''
ON CONFLICT (kind, target) WHERE state = 'pending' DO NOTHING;

-- +goose Down
ALTER TABLE items DROP COLUMN age_rating;
ALTER TABLE profiles DROP COLUMN block_unrated;
ALTER TABLE profiles DROP COLUMN max_age;
ALTER TABLE accounts DROP COLUMN block_unrated;
ALTER TABLE accounts DROP COLUMN max_age;
DROP TABLE account_libraries;
ALTER TABLE accounts DROP COLUMN all_libraries;
