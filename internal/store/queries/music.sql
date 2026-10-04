-- Music: artists, albums, tracks.

-- name: UpsertTrack :exec
INSERT INTO tracks (item_id, album_id, artist_id, disc, number, artists, track_gain, track_peak, album_gain, album_peak)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (item_id) DO UPDATE
SET album_id = excluded.album_id,
    artist_id = excluded.artist_id,
    disc = excluded.disc,
    number = excluded.number,
    artists = excluded.artists,
    track_gain = excluded.track_gain,
    track_peak = excluded.track_peak,
    album_gain = excluded.album_gain,
    album_peak = excluded.album_peak;

-- name: GetTrack :one
SELECT * FROM tracks WHERE item_id = ?;

-- name: ListAlbumTrackIDs :many
SELECT item_id FROM tracks WHERE album_id = ? ORDER BY disc, number;

-- name: ListArtistTrackIDs :many
SELECT t.item_id
FROM tracks t
JOIN items a ON a.id = t.album_id
WHERE t.artist_id = ?
ORDER BY a.year, a.sort_title, t.disc, t.number;

-- name: ListAlbumFiles :many
SELECT f.id, f.path
FROM media_files f
JOIN item_files l ON l.file_id = f.id
JOIN tracks t ON t.item_id = l.item_id
WHERE t.album_id = ? AND f.missing_since IS NULL
ORDER BY t.disc, t.number;

-- name: FirstFileOfArtist :one
SELECT f.id, f.path
FROM media_files f
JOIN item_files l ON l.file_id = f.id
JOIN tracks t ON t.item_id = l.item_id
JOIN items a ON a.id = t.album_id
WHERE t.artist_id = ? AND f.missing_since IS NULL
ORDER BY a.year, a.sort_title, t.disc, t.number
LIMIT 1;

-- name: AlbumTrackSummary :one
SELECT CAST(COALESCE(SUM(i.runtime_ms), 0) AS INTEGER) AS runtime_ms, CAST(COALESCE(MAX(i.year), 0) AS INTEGER) AS year
FROM tracks t
JOIN items i ON i.id = t.item_id
WHERE t.album_id = ? AND EXISTS (
    SELECT 1 FROM item_files l JOIN media_files f ON f.id = l.file_id
    WHERE l.item_id = t.item_id AND f.missing_since IS NULL
);

-- name: ListAlbumTrackGenres :many
SELECT g.genre
FROM item_genres g
JOIN tracks t ON t.item_id = g.item_id
WHERE t.album_id = ?
GROUP BY g.genre
ORDER BY COUNT(*) DESC, g.genre;
