-- name: UpsertPlayed :exec
INSERT INTO user_data (profile_id, item_id, played, position_ms, last_played_at, updated_at)
VALUES (?, ?, ?, 0, ?, ?)
ON CONFLICT (profile_id, item_id) DO UPDATE
SET played = excluded.played,
    position_ms = 0,
    last_played_at = COALESCE(excluded.last_played_at, user_data.last_played_at),
    updated_at = excluded.updated_at;

-- name: UpsertFavorite :exec
INSERT INTO user_data (profile_id, item_id, favorite, updated_at) VALUES (?, ?, ?, ?)
ON CONFLICT (profile_id, item_id) DO UPDATE
SET favorite = excluded.favorite, updated_at = excluded.updated_at;

-- name: ListSeriesEpisodeIDs :many
SELECT item_id FROM episodes WHERE series_id = ?;

-- name: ListSeasonEpisodeIDs :many
SELECT item_id FROM episodes WHERE season_id = ?;

-- name: SaveResumePosition :exec
INSERT INTO user_data (profile_id, item_id, position_ms, last_played_at, updated_at) VALUES (?, ?, ?, ?, ?)
ON CONFLICT (profile_id, item_id) DO UPDATE
SET position_ms = excluded.position_ms,
    last_played_at = excluded.last_played_at,
    updated_at = excluded.updated_at;

-- name: SaveFinished :exec
INSERT INTO user_data (profile_id, item_id, played, play_count, position_ms, last_played_at, updated_at)
VALUES (?, ?, 1, 1, 0, ?, ?)
ON CONFLICT (profile_id, item_id) DO UPDATE
SET played = 1,
    play_count = user_data.play_count + 1,
    position_ms = 0,
    last_played_at = excluded.last_played_at,
    updated_at = excluded.updated_at;

-- name: ListSeasonIDs :many
SELECT item_id FROM seasons WHERE series_id = ? ORDER BY number;

-- name: GetUserData :one
SELECT played, play_count, position_ms, last_played_at, favorite FROM user_data WHERE profile_id = ? AND item_id = ?;

-- name: UpsertUserData :exec
INSERT INTO user_data (profile_id, item_id, played, play_count, position_ms, last_played_at, favorite, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (profile_id, item_id) DO UPDATE
SET played = excluded.played,
    play_count = excluded.play_count,
    position_ms = excluded.position_ms,
    last_played_at = excluded.last_played_at,
    favorite = excluded.favorite,
    updated_at = excluded.updated_at;
