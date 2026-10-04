-- Photos: photos and albums.

-- name: UpsertPhoto :exec
INSERT INTO photos (item_id, taken_at, utc_offset, width, height, make, model, lens, f_number, exposure_time, iso,
                    focal_length, latitude, longitude)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (item_id) DO UPDATE
SET taken_at = excluded.taken_at,
    utc_offset = excluded.utc_offset,
    width = excluded.width,
    height = excluded.height,
    make = excluded.make,
    model = excluded.model,
    lens = excluded.lens,
    f_number = excluded.f_number,
    exposure_time = excluded.exposure_time,
    iso = excluded.iso,
    focal_length = excluded.focal_length,
    latitude = excluded.latitude,
    longitude = excluded.longitude;

-- name: GetPhoto :one
SELECT * FROM photos WHERE item_id = ?;

-- name: DeleteEmptyPhotoAlbums :execrows
DELETE FROM items
WHERE items.library_id = ? AND items.kind = 'photo_album'
  AND NOT EXISTS (SELECT 1 FROM items c WHERE c.parent_id = items.id);
