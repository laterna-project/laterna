package store

import (
	"context"
	"encoding/json"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/store/sqlc"
)

// Books.

// SetBook stores the book-specific part of an item.
func (q Q) SetBook(ctx context.Context, b domain.Book) error {
	return q.q.UpsertBook(ctx, sqlc.UpsertBookParams{
		ItemID: b.ItemID, Number: b.Number, Publisher: b.Publisher, Language: b.Language, Isbn: b.ISBN,
	})
}

// Book reads the book-specific part of an item.
func (q Q) Book(ctx context.Context, itemID domain.ID) (domain.Book, error) {
	r, err := q.q.GetBook(ctx, itemID)
	if err != nil {
		return domain.Book{}, err
	}
	return domain.Book{ItemID: r.ItemID, Number: r.Number, Publisher: r.Publisher, Language: r.Language, ISBN: r.Isbn}, nil
}

// SetBookFile stores how to read a book file.
func (q Q) SetBookFile(ctx context.Context, f domain.BookFile) error {
	pages := make([][2]int, len(f.Pages))
	for i, p := range f.Pages {
		pages[i] = [2]int{p.Width, p.Height}
	}
	raw, err := json.Marshal(pages)
	if err != nil {
		return err
	}
	return q.q.UpsertBookFile(ctx, sqlc.UpsertBookFileParams{
		FileID: f.FileID, Format: string(f.Format), Layout: string(f.Layout), RightToLeft: toInt(f.RightToLeft),
		PageCount: int64(f.PageCount), Pages: string(raw), AccessKey: f.Key,
	})
}

// BookFile reads how to read a book file.
func (q Q) BookFile(ctx context.Context, fileID domain.ID) (domain.BookFile, error) {
	r, err := q.q.GetBookFile(ctx, fileID)
	if err != nil {
		return domain.BookFile{}, err
	}
	var pages [][2]int
	if err := json.Unmarshal([]byte(r.Pages), &pages); err != nil {
		return domain.BookFile{}, err
	}
	f := domain.BookFile{
		FileID: r.FileID, Format: domain.BookFormat(r.Format), Layout: domain.BookLayout(r.Layout),
		RightToLeft: r.RightToLeft == 1, PageCount: int(r.PageCount), Key: r.AccessKey,
	}
	for _, p := range pages {
		f.Pages = append(f.Pages, domain.PageSize{Width: p[0], Height: p[1]})
	}
	return f, nil
}

// SeriesBookIDs lists the books of a series (present or not), in volume order.
func (q Q) SeriesBookIDs(ctx context.Context, seriesID domain.ID) ([]domain.ID, error) {
	return q.q.ListSeriesBookIDs(ctx, &seriesID)
}

// FirstBookOfSeries returns the first volume of a series that is present.
func (q Q) FirstBookOfSeries(ctx context.Context, seriesID domain.ID) (domain.ID, error) {
	return q.q.FirstBookOfSeries(ctx, &seriesID)
}

// SetReadingProgress stores where a profile is in a book.
func (q Q) SetReadingProgress(ctx context.Context, profileID, itemID domain.ID, p domain.ReadingProgress) error {
	return q.q.UpsertReadingProgress(ctx, sqlc.UpsertReadingProgressParams{
		ProfileID: profileID, ItemID: itemID, Page: int64(p.Page), Locator: p.Locator, Progression: p.Progression,
		UpdatedAt: toMillis(p.UpdatedAt),
	})
}

// ReadingProgress reads where a profile is in a book.
func (q Q) ReadingProgress(ctx context.Context, profileID, itemID domain.ID) (domain.ReadingProgress, error) {
	r, err := q.q.GetReadingProgress(ctx, sqlc.GetReadingProgressParams{ProfileID: profileID, ItemID: itemID})
	if err != nil {
		return domain.ReadingProgress{}, err
	}
	return readingFromRow(r.Page, r.Locator, r.Progression, r.UpdatedAt), nil
}

// ClearReadingProgress forgets where a profile was in a book (read again from the start).
func (q Q) ClearReadingProgress(ctx context.Context, profileID, itemID domain.ID) error {
	return q.q.DeleteReadingProgress(ctx, sqlc.DeleteReadingProgressParams{ProfileID: profileID, ItemID: itemID})
}

func readingFromRow(page int64, locator string, progression float64, updatedAt int64) domain.ReadingProgress {
	return domain.ReadingProgress{Page: int(page), Locator: locator, Progression: progression, UpdatedAt: fromMillis(updatedAt)}
}

// SeriesBooks lists the books of a series that are present, in volume order.
func (q Q) SeriesBooks(ctx context.Context, v domain.Viewer, seriesID domain.ID) ([]domain.ItemView, error) {
	b := cardQuery(v)
	b.add(" JOIN books bk ON bk.item_id = i.id")
	b.where("i.kind = 'book'").where("i.parent_id = ?", seriesID).where(presentCond(domain.ItemBook)).flushWhere()
	b.add(" ORDER BY bk.number, i.sort_title")
	return q.cards(ctx, b)
}

// ReadingNow lists the books a profile has started and not finished, most recently read first.
func (q Q) ReadingNow(ctx context.Context, v domain.Viewer, limit int) ([]domain.ItemView, error) {
	b := cardQuery(v)
	b.add(" JOIN reading_progress rp ON rp.item_id = i.id AND rp.profile_id = ?", v.ProfileID)
	b.where("i.kind = 'book'").where("COALESCE(u.played, 0) = 0").where(presentCond(domain.ItemBook)).flushWhere()
	b.add(" ORDER BY rp.updated_at DESC, i.id LIMIT ?", limit)
	return q.cards(ctx, b)
}

// attachBooks fills in book cards (volume, publisher, language, series, the profile's progress) and
// book series cards (books present; read if all of them are), in two queries.
func (q Q) attachBooks(ctx context.Context, profileID domain.ID, cards []domain.ItemView) error {
	if len(cards) == 0 {
		return nil
	}
	var books, series []any
	index := map[domain.ID]int{}
	for i, c := range cards {
		switch c.Item.Kind {
		case domain.ItemBook:
			books = append(books, c.Item.ID)
		case domain.ItemBookSeries:
			series = append(series, c.Item.ID)
		case domain.ItemMovie, domain.ItemSeries, domain.ItemSeason, domain.ItemEpisode, domain.ItemArtist,
			domain.ItemAlbum, domain.ItemTrack, domain.ItemPhotoAlbum, domain.ItemPhoto:
			continue
		}
		index[c.Item.ID] = i
	}
	if len(books) > 0 {
		var b query
		b.add(`SELECT bk.item_id, bk.number, bk.publisher, bk.language, bk.isbn, COALESCE(s.title, ''),
			rp.page, rp.locator, rp.progression, rp.updated_at
			FROM books bk JOIN items i ON i.id = bk.item_id LEFT JOIN items s ON s.id = i.parent_id
			LEFT JOIN reading_progress rp ON rp.item_id = bk.item_id AND rp.profile_id = ?
			WHERE bk.item_id IN (`+placeholders(len(books))+`)`, append([]any{profileID}, books...)...)
		rows, err := q.db.QueryContext(ctx, b.String(), b.args...)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var (
				bk          domain.Book
				seriesTitle string
				page        *int64
				locator     *string
				progression *float64
				updated     *int64
			)
			if err := rows.Scan(&bk.ItemID, &bk.Number, &bk.Publisher, &bk.Language, &bk.ISBN, &seriesTitle,
				&page, &locator, &progression, &updated); err != nil {
				return err
			}
			c := &cards[index[bk.ItemID]]
			c.Book, c.SeriesTitle = &bk, seriesTitle
			if page != nil && locator != nil && progression != nil && updated != nil {
				rp := readingFromRow(*page, *locator, *progression, *updated)
				c.Reading = &rp
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}
	}
	if len(series) > 0 {
		var b query
		b.add(`SELECT i.parent_id, COUNT(*), SUM(COALESCE(u.played, 0)) FROM items i
			LEFT JOIN user_data u ON u.item_id = i.id AND u.profile_id = ?
			WHERE i.kind = 'book' AND i.parent_id IN (`+placeholders(len(series))+`) AND `+
			presentHere+` GROUP BY i.parent_id`, append([]any{profileID}, series...)...)
		rows, err := q.db.QueryContext(ctx, b.String(), b.args...)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var id domain.ID
			var n, read int
			if err := rows.Scan(&id, &n, &read); err != nil {
				return err
			}
			// A series is read once all its books are.
			c := &cards[index[id]]
			c.BookCount, c.UserData.Played = n, n > 0 && read == n
		}
		return rows.Err()
	}
	return nil
}
