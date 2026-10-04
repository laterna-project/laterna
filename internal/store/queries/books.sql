-- Books: books, book files, reading progress.

-- name: UpsertBook :exec
INSERT INTO books (item_id, number, publisher, language, isbn)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT (item_id) DO UPDATE
SET number = excluded.number,
    publisher = excluded.publisher,
    language = excluded.language,
    isbn = excluded.isbn;

-- name: GetBook :one
SELECT * FROM books WHERE item_id = ?;

-- name: UpsertBookFile :exec
INSERT INTO book_files (file_id, format, layout, right_to_left, page_count, pages, access_key)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (file_id) DO UPDATE
SET format = excluded.format,
    layout = excluded.layout,
    right_to_left = excluded.right_to_left,
    page_count = excluded.page_count,
    pages = excluded.pages,
    access_key = excluded.access_key;

-- name: GetBookFile :one
SELECT * FROM book_files WHERE file_id = ?;

-- name: ListSeriesBookIDs :many
SELECT i.id
FROM items i
JOIN books b ON b.item_id = i.id
WHERE i.parent_id = ? AND i.kind = 'book'
ORDER BY b.number, i.sort_title;

-- name: FirstBookOfSeries :one
SELECT i.id
FROM items i
JOIN books b ON b.item_id = i.id
WHERE i.parent_id = ? AND i.kind = 'book' AND EXISTS (
    SELECT 1 FROM item_files l JOIN media_files f ON f.id = l.file_id
    WHERE l.item_id = i.id AND f.missing_since IS NULL
)
ORDER BY b.number, i.sort_title
LIMIT 1;

-- name: DeleteEmptyBookSeries :execrows
DELETE FROM items
WHERE items.library_id = ? AND items.kind = 'book_series'
  AND NOT EXISTS (SELECT 1 FROM items b WHERE b.parent_id = items.id);

-- name: UpsertReadingProgress :exec
INSERT INTO reading_progress (profile_id, item_id, page, locator, progression, updated_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (profile_id, item_id) DO UPDATE
SET page = excluded.page,
    locator = excluded.locator,
    progression = excluded.progression,
    updated_at = excluded.updated_at;

-- name: GetReadingProgress :one
SELECT * FROM reading_progress WHERE profile_id = ? AND item_id = ?;

-- name: DeleteReadingProgress :exec
DELETE FROM reading_progress WHERE profile_id = ? AND item_id = ?;
