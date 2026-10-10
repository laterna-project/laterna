-- Requests for movies and series, and where they land (docs/design/requests.md).

-- name: InsertRequestDestination :exec
INSERT INTO request_destinations (id, name, name_key, kind, library_id, root_folder, quality_profile_id,
                                  quality_profile_name, series_type, metadata_profile_id, metadata_profile_name,
                                  created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: UpdateRequestDestination :exec
UPDATE request_destinations
SET name = ?, name_key = ?, library_id = ?, root_folder = ?, quality_profile_id = ?, quality_profile_name = ?,
    series_type = ?, metadata_profile_id = ?, metadata_profile_name = ?, updated_at = ?
WHERE id = ?;

-- name: DeleteRequestDestination :exec
DELETE FROM request_destinations WHERE id = ?;

-- name: ListRequestDestinations :many
SELECT d.id, d.name, d.kind, d.library_id, l.name AS library_name, d.root_folder, d.quality_profile_id,
       d.quality_profile_name, d.series_type, d.metadata_profile_id, d.metadata_profile_name, d.created_at,
       d.updated_at
FROM request_destinations d
JOIN libraries l ON l.id = d.library_id
ORDER BY d.kind, d.name_key;

-- name: InsertRequest :exec
INSERT INTO requests (id, kind, external_id, external_key, title, subtitle, year, poster, status, seasons,
                      season_numbers, destination_id, account_id, profile_id, created_at, updated_at, decided_at,
                      decided_by)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: UpdateRequest :exec
UPDATE requests
SET status = ?, seasons = ?, season_numbers = ?, destination_id = ?, updated_at = ?, decided_at = ?, decided_by = ?,
    decline_reason = ?, error = ?, arr_id = ?, progress = ?, item_id = ?, episodes_available = ?,
    episodes_wanted = ?, available_at = ?
WHERE id = ?;

-- name: RequestPosters :many
SELECT DISTINCT poster FROM requests WHERE poster <> '';

-- name: DeleteRequest :exec
DELETE FROM requests WHERE id = ?;

-- name: CountRequestsSince :one
SELECT count(*) FROM requests WHERE account_id = ? AND created_at >= ?;

-- name: CountPendingRequests :one
SELECT count(*) FROM requests WHERE status = 'pending';

-- name: OpenRequestFor :one
SELECT id FROM requests
WHERE kind = ? AND external_id = ? AND external_key = ? AND status IN ('pending', 'approved', 'downloading');

-- name: RequestsOnTheirWay :many
SELECT id FROM requests
WHERE status IN ('approved', 'downloading')
   OR (status = 'available' AND kind IN ('series', 'artist') AND episodes_available < episodes_wanted
       AND available_at >= ?)
ORDER BY created_at;

-- name: DeclinePendingRequestsTo :many
UPDATE requests
SET status = 'declined', updated_at = sqlc.arg(now), decided_at = sqlc.arg(now), decided_by = ''
WHERE destination_id = sqlc.arg(destination_id) AND status = 'pending'
RETURNING id, profile_id;

-- name: ItemWithExternalID :many
SELECT p.item_id, i.library_id
FROM provider_ids p
JOIN items i ON i.id = p.item_id
WHERE p.provider = ? AND p.value = ?
  AND ((i.kind = 'movie' AND i.present = 1)
    OR (i.kind = 'series' AND EXISTS (SELECT 1 FROM episodes e JOIN items ei ON ei.id = e.item_id
                                      WHERE e.series_id = i.id AND ei.present = 1))
    OR (i.kind = 'album' AND EXISTS (SELECT 1 FROM tracks t JOIN items ti ON ti.id = t.item_id
                                     WHERE t.album_id = i.id AND ti.present = 1))
    OR (i.kind = 'artist' AND EXISTS (SELECT 1 FROM tracks t JOIN items ti ON ti.id = t.item_id
                                      WHERE t.artist_id = i.id AND ti.present = 1)));
