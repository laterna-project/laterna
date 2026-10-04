-- Playback history: one row per playback of a profile (movie, episode, track), with the time
-- actually watched. The title is kept as it was: the history outlives a forgotten file (the item
-- becomes NULL).

-- +goose Up
CREATE TABLE play_history (
    id          BLOB PRIMARY KEY NOT NULL,
    profile_id  BLOB NOT NULL REFERENCES profiles (id) ON DELETE CASCADE,
    item_id     BLOB REFERENCES items (id) ON DELETE SET NULL,
    kind        TEXT NOT NULL CHECK (kind IN ('movie', 'episode', 'track')),
    -- Series of an episode; album and (album) artist of a track.
    series_id   BLOB REFERENCES items (id) ON DELETE SET NULL,
    album_id    BLOB REFERENCES items (id) ON DELETE SET NULL,
    artist_id   BLOB REFERENCES items (id) ON DELETE SET NULL,
    title       TEXT NOT NULL,
    -- Series of an episode, artists of a track; empty for a movie.
    subtitle    TEXT NOT NULL,
    started_at  INTEGER NOT NULL,
    ended_at    INTEGER NOT NULL CHECK (ended_at >= started_at),
    watched_ms  INTEGER NOT NULL CHECK (watched_ms >= 0),
    position_ms INTEGER NOT NULL CHECK (position_ms >= 0),
    duration_ms INTEGER NOT NULL CHECK (duration_ms >= 0),
    completed   INTEGER NOT NULL CHECK (completed IN (0, 1)),
    device      TEXT NOT NULL,
    offline     INTEGER NOT NULL CHECK (offline IN (0, 1))
) STRICT;

-- History of a profile, newest first; statistics for a period.
CREATE INDEX play_history_profile ON play_history (profile_id, started_at, id);

-- Foreign keys: deleting an item does not scan the whole history.
CREATE INDEX play_history_item ON play_history (item_id) WHERE item_id IS NOT NULL;

CREATE INDEX play_history_series ON play_history (series_id) WHERE series_id IS NOT NULL;

CREATE INDEX play_history_album ON play_history (album_id) WHERE album_id IS NOT NULL;

CREATE INDEX play_history_artist ON play_history (artist_id) WHERE artist_id IS NOT NULL;

-- +goose Down
DROP TABLE play_history;
