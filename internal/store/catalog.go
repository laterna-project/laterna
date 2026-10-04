package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/store/sqlc"
)

// Cursor marks the end of a page: the sort value and the ID of the last item. Text or Num is used
// depending on the sort order.
type Cursor struct {
	Text string    `json:"t,omitempty"`
	Num  float64   `json:"n,omitempty"`
	ID   domain.ID `json:"i"`
}

// ItemQuery describes a page of movies, series, artists, albums or tracks.
type ItemQuery struct {
	Kind domain.ItemKind
	// LibraryID restricts to one library; nil means all.
	LibraryID *domain.ID
	// ParentID restricts to the children of an item (the albums of an artist); nil means all.
	ParentID *domain.ID
	// Viewer is the profile we read for, and what it is allowed to see.
	Viewer domain.Viewer
	Genre  string
	// Played filters on the played state (movies only).
	Played   *bool
	Favorite bool
	Sort     domain.ItemSort
	// Reverse flips the natural direction of the sort order.
	Reverse bool
	After   *Cursor
	Limit   int
}

type sortSpec struct {
	expr string
	desc bool // natural direction
	num  bool // numeric value (Cursor.Num) or text (Cursor.Text)
}

// An unknown release date falls back to the year ("2020" sorts before "2020-05-01").
var sorts = map[domain.ItemSort]sortSpec{
	domain.SortTitle:    {expr: "i.sort_title"},
	domain.SortAdded:    {expr: "i.added_at", desc: true, num: true},
	domain.SortReleased: {expr: "CASE WHEN i.premiere_date <> '' THEN i.premiere_date ELSE printf('%04d', i.year) END", desc: true},
	domain.SortRating:   {expr: "i.community_rating", desc: true, num: true},
}

// An item only shows up in lists if at least one of its files is present. A vanished file stays
// known during the grace period but is no longer visible. items.present says so for items that have
// files and is maintained by triggers; the other kinds derive it from their children.
const (
	// presentHere is for the item of the query ("i"): answered by the index, nothing else to look
	// up.
	presentHere = "i.present = 1"
	// presentFileOf is for the item with ID %s.
	presentFileOf    = `EXISTS (SELECT 1 FROM items pf WHERE pf.id = %s AND pf.present = 1)`
	presentEpisodeOf = `EXISTS (SELECT 1 FROM episodes pe JOIN items pi ON pi.id = pe.item_id
		WHERE pe.%s = i.id AND pi.present = 1)`
	// Artist or album: one of its tracks has a file present (column of tracks, item).
	presentTrackOf = `EXISTS (SELECT 1 FROM tracks pt JOIN items pi ON pi.id = pt.item_id
		WHERE pt.%s = %s AND pi.present = 1)`
	// Book series: one of its books has a file present.
	presentChildOf = `EXISTS (SELECT 1 FROM items pc WHERE pc.parent_id = %s AND pc.present = 1)`
	// Photo album: one of its photos has a file present, or it contains an album (empty albums are
	// deleted).
	presentAlbumOf = `(EXISTS (SELECT 1 FROM items pa WHERE pa.parent_id = %[1]s AND pa.kind = 'photo_album') OR ` +
		`EXISTS (SELECT 1 FROM items pc WHERE pc.parent_id = %[1]s AND pc.present = 1))`
)

func presentCond(kind domain.ItemKind) string {
	switch kind {
	case domain.ItemSeries:
		return fmt.Sprintf(presentEpisodeOf, "series_id")
	case domain.ItemSeason:
		return fmt.Sprintf(presentEpisodeOf, "season_id")
	case domain.ItemArtist:
		return fmt.Sprintf(presentTrackOf, "artist_id", "i.id")
	case domain.ItemAlbum:
		return fmt.Sprintf(presentTrackOf, "album_id", "i.id")
	case domain.ItemBookSeries:
		return fmt.Sprintf(presentChildOf, "i.id")
	case domain.ItemPhotoAlbum:
		return fmt.Sprintf(presentAlbumOf, "i.id")
	case domain.ItemMovie, domain.ItemEpisode, domain.ItemTrack, domain.ItemBook, domain.ItemPhoto:
	}
	return presentHere
}

// ageOf is the age of an item's rating, or failing that the one of its series (seasons and
// episodes); -1 if unrated.
const ageOf = `COALESCE(i.age_rating, (SELECT sa.age_rating FROM items sa WHERE sa.id = COALESCE(
	(SELECT ae.series_id FROM episodes ae WHERE ae.item_id = i.id),
	(SELECT as2.series_id FROM seasons as2 WHERE as2.item_id = i.id))), -1)`

// cond is an SQL condition and its arguments.
type cond struct {
	sql  string
	args []any
}

// viewerConds returns the conditions for what a profile is allowed to see: the libraries of its
// account, and the allowed ratings. Music, books and photos have no rating, so age does not apply
// to them. The item of the query is "i".
func viewerConds(v domain.Viewer) []cond {
	var out []cond
	if v.Libraries != nil {
		if len(v.Libraries) == 0 {
			out = append(out, cond{sql: "0"})
		} else {
			args := make([]any, len(v.Libraries))
			for i, id := range v.Libraries {
				args[i] = id
			}
			out = append(out, cond{sql: "i.library_id IN (" + placeholders(len(args)) + ")", args: args})
		}
	}
	const music = "i.kind IN ('artist', 'album', 'track', 'book_series', 'book', 'photo_album', 'photo') OR "
	switch p := v.Parental; {
	case p.MaxAge != nil && p.BlockUnrated:
		out = append(out, cond{sql: "(" + music + ageOf + " BETWEEN 0 AND ?)", args: []any{*p.MaxAge}})
	case p.MaxAge != nil:
		out = append(out, cond{sql: "(" + music + ageOf + " <= ?)", args: []any{*p.MaxAge}})
	case p.BlockUnrated:
		out = append(out, cond{sql: "(" + music + ageOf + " >= 0)"})
	}
	return out
}

// viewerFilter adds to a query what a profile is allowed to see.
func viewerFilter(b *query, v domain.Viewer) {
	for _, c := range viewerConds(v) {
		b.where(c.sql, c.args...)
	}
}

// viewerAnd writes a profile's conditions as a continuation of a WHERE clause ("AND ..."), for a
// subquery.
func viewerAnd(v domain.Viewer) (string, []any) {
	var sb strings.Builder
	var args []any
	for _, c := range viewerConds(v) {
		sb.WriteString(" AND " + c.sql)
		args = append(args, c.args...)
	}
	return sb.String(), args
}

// presentAny matches an item that has a file present, directly or through its episodes or tracks
// ("i").
var presentAny = "(CASE i.kind WHEN 'series' THEN " + presentCond(domain.ItemSeries) +
	" WHEN 'artist' THEN " + presentCond(domain.ItemArtist) + " WHEN 'album' THEN " + presentCond(domain.ItemAlbum) +
	" WHEN 'book_series' THEN " + presentCond(domain.ItemBookSeries) + " WHEN 'photo_album' THEN " + presentCond(domain.ItemPhotoAlbum) +
	" ELSE " + presentCond(domain.ItemMovie) + " END)"

// cardQuery starts a card query for a profile: item columns, the profile's data, season, episode or
// track, episode counts (series and seasons), album and track counts (artists and albums), with the
// conditions for what it is allowed to see. The caller adds its own joins, the other conditions,
// the order and the limit.
func cardQuery(v domain.Viewer) *query {
	profileID := v.ProfileID
	var b query
	b.add(`SELECT i.id, i.library_id, i.kind, i.parent_id, i.group_key, i.title, i.sort_title,
		i.original_title, i.year, i.premiere_date, i.overview, i.tagline, i.official_rating,
		i.community_rating, i.runtime_ms, i.metadata_at, i.added_at, i.updated_at,
		COALESCE(u.played, 0), COALESCE(u.play_count, 0), COALESCE(u.position_ms, 0),
		u.last_played_at, COALESCE(u.favorite, 0),
		COALESCE(s.series_id, e.series_id), COALESCE(s.number, 0), e.season_id,
		COALESCE(e.season_number, 0), COALESCE(e.number, 0), COALESCE(e.number_end, 0),
		COALESCE(e.absolute, 0), COALESCE(sr.title, ''),
		CASE i.kind
			WHEN 'series' THEN (SELECT COUNT(*) FROM episodes ce WHERE ce.series_id = i.id AND `+fmt.Sprintf(presentFileOf, "ce.item_id")+`)
			WHEN 'season' THEN (SELECT COUNT(*) FROM episodes ce WHERE ce.season_id = i.id AND `+fmt.Sprintf(presentFileOf, "ce.item_id")+`)
			ELSE 0 END,
		CASE i.kind
			WHEN 'series' THEN (SELECT COUNT(*) FROM episodes ce LEFT JOIN user_data cu ON cu.item_id = ce.item_id AND cu.profile_id = ?
				WHERE ce.series_id = i.id AND COALESCE(cu.played, 0) = 0 AND `+fmt.Sprintf(presentFileOf, "ce.item_id")+`)
			WHEN 'season' THEN (SELECT COUNT(*) FROM episodes ce LEFT JOIN user_data cu ON cu.item_id = ce.item_id AND cu.profile_id = ?
				WHERE ce.season_id = i.id AND COALESCE(cu.played, 0) = 0 AND `+fmt.Sprintf(presentFileOf, "ce.item_id")+`)
			ELSE 0 END,
		t.album_id, t.artist_id, COALESCE(t.disc, 0), COALESCE(t.number, 0), COALESCE(t.artists, ''),
		t.track_gain, t.track_peak, t.album_gain, t.album_peak, COALESCE(tal.title, ''), COALESCE(tar.title, ''),
		CASE i.kind
			WHEN 'artist' THEN (SELECT COUNT(*) FROM items ca WHERE ca.parent_id = i.id AND ca.kind = 'album' AND `+fmt.Sprintf(presentTrackOf, "album_id", "ca.id")+`)
			ELSE 0 END,
		CASE i.kind
			WHEN 'artist' THEN (SELECT COUNT(*) FROM tracks ct WHERE ct.artist_id = i.id AND `+fmt.Sprintf(presentFileOf, "ct.item_id")+`)
			WHEN 'album' THEN (SELECT COUNT(*) FROM tracks ct WHERE ct.album_id = i.id AND `+fmt.Sprintf(presentFileOf, "ct.item_id")+`)
			ELSE 0 END
		FROM items i
		LEFT JOIN user_data u ON u.item_id = i.id AND u.profile_id = ?
		LEFT JOIN seasons s ON s.item_id = i.id
		LEFT JOIN episodes e ON e.item_id = i.id
		LEFT JOIN items sr ON sr.id = e.series_id
		LEFT JOIN tracks t ON t.item_id = i.id
		LEFT JOIN items tal ON tal.id = t.album_id
		LEFT JOIN items tar ON tar.id = CASE i.kind WHEN 'album' THEN i.parent_id ELSE t.artist_id END`, profileID, profileID, profileID)
	viewerFilter(&b, v)
	b.profile = profileID
	return &b
}

// cards runs a card query and attaches the images.
func (q Q) cards(ctx context.Context, b *query) ([]domain.ItemView, error) {
	rows, err := q.db.QueryContext(ctx, b.String(), b.args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []domain.ItemView
	for rows.Next() {
		var (
			r                                    sqlc.Item
			played, playCount, positionMs, fav   int64
			lastPlayed                           sql.NullInt64
			seriesID, seasonID                   *domain.ID
			seasonNum, epSeason, epNum, epEnd    int64
			absolute, episodeCount, unplayedCnt  int64
			albumID, artistID                    *domain.ID
			disc, trackNum, albumCount, trackCnt int64
			trackGain, trackPeak                 sql.NullFloat64
			albumGain, albumPeak                 sql.NullFloat64
			artists                              string
			c                                    domain.ItemView
		)
		if err := rows.Scan(&r.ID, &r.LibraryID, &r.Kind, &r.ParentID, &r.GroupKey, &r.Title, &r.SortTitle,
			&r.OriginalTitle, &r.Year, &r.PremiereDate, &r.Overview, &r.Tagline, &r.OfficialRating,
			&r.CommunityRating, &r.RuntimeMs, &r.MetadataAt, &r.AddedAt, &r.UpdatedAt,
			&played, &playCount, &positionMs, &lastPlayed, &fav,
			&seriesID, &seasonNum, &seasonID, &epSeason, &epNum, &epEnd, &absolute, &c.SeriesTitle,
			&episodeCount, &unplayedCnt,
			&albumID, &artistID, &disc, &trackNum, &artists, &trackGain, &trackPeak, &albumGain, &albumPeak,
			&c.AlbumTitle, &c.ArtistName, &albumCount, &trackCnt); err != nil {
			return nil, err
		}
		c.Item = itemFromRow(r)
		c.UserData = domain.UserData{
			Played: played == 1, PlayCount: int(playCount), Position: time.Duration(positionMs) * time.Millisecond,
			LastPlayedAt: optTime(lastPlayed), Favorite: fav == 1,
		}
		c.EpisodeCount, c.UnplayedCount = int(episodeCount), int(unplayedCnt)
		c.AlbumCount, c.TrackCount = int(albumCount), int(trackCnt)
		switch c.Item.Kind {
		case domain.ItemSeries:
			c.UserData.Played = c.EpisodeCount > 0 && c.UnplayedCount == 0
		case domain.ItemSeason:
			c.UserData.Played = c.EpisodeCount > 0 && c.UnplayedCount == 0
			if seriesID != nil {
				c.Season = &domain.Season{ItemID: c.Item.ID, SeriesID: *seriesID, Number: int(seasonNum)}
			}
		case domain.ItemEpisode:
			if seriesID != nil && seasonID != nil {
				c.Episode = &domain.Episode{
					ItemID: c.Item.ID, SeriesID: *seriesID, SeasonID: *seasonID, SeasonNumber: int(epSeason),
					Number: int(epNum), NumberEnd: int(epEnd), Absolute: absolute == 1,
				}
			}
		case domain.ItemTrack:
			if albumID != nil && artistID != nil {
				c.Track = &domain.Track{
					ItemID: c.Item.ID, AlbumID: *albumID, ArtistID: *artistID, Disc: int(disc), Number: int(trackNum),
					Artists: artists, TrackGain: optFloat(trackGain), TrackPeak: optFloat(trackPeak),
					AlbumGain: optFloat(albumGain), AlbumPeak: optFloat(albumPeak),
				}
			}
		case domain.ItemMovie, domain.ItemArtist, domain.ItemAlbum, domain.ItemBookSeries, domain.ItemBook,
			domain.ItemPhotoAlbum, domain.ItemPhoto:
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := q.attachBooks(ctx, b.profile, out); err != nil {
		return nil, err
	}
	if err := q.attachPhotos(ctx, out); err != nil {
		return nil, err
	}
	return out, q.attachImages(ctx, out)
}

// attachImages attaches the analyzed images to the cards, in one query. A track gets the cover of
// its album.
func (q Q) attachImages(ctx context.Context, cards []domain.ItemView) error {
	if len(cards) == 0 {
		return nil
	}
	ids := make([]any, 0, len(cards))
	index := make(map[domain.ID][]int, len(cards))
	for i, c := range cards {
		owner := c.Item.ID
		if c.Track != nil {
			owner = c.Track.AlbumID
		}
		if _, seen := index[owner]; !seen {
			ids = append(ids, owner)
		}
		index[owner] = append(index[owner], i)
	}
	var b query
	b.add(`SELECT id, item_id, person_id, kind, source, path, remote_url, width, height, blurhash, hash, updated_at
		FROM images WHERE hash <> '' AND item_id IN (`+placeholders(len(ids))+`) ORDER BY kind`, ids...)
	rows, err := q.db.QueryContext(ctx, b.String(), b.args...)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var r sqlc.Image
		if err := rows.Scan(&r.ID, &r.ItemID, &r.PersonID, &r.Kind, &r.Source, &r.Path, &r.RemoteUrl,
			&r.Width, &r.Height, &r.Blurhash, &r.Hash, &r.UpdatedAt); err != nil {
			return err
		}
		if r.ItemID != nil {
			for _, i := range index[*r.ItemID] {
				cards[i].Images = append(cards[i].Images, imageFromRow(r))
			}
		}
	}
	return rows.Err()
}

// itemFilters adds the conditions shared by a page and its count.
func itemFilters(b *query, iq ItemQuery) {
	b.where("i.kind = ?", string(iq.Kind))
	if iq.LibraryID != nil {
		b.where("i.library_id = ?", *iq.LibraryID)
	}
	if iq.ParentID != nil {
		b.where("i.parent_id = ?", *iq.ParentID)
	}
	b.where(presentCond(iq.Kind))
	if iq.Genre != "" {
		b.where("EXISTS (SELECT 1 FROM item_genres g WHERE g.item_id = i.id AND g.genre = ? COLLATE NOCASE)", iq.Genre)
	}
	if iq.Played != nil {
		b.where("COALESCE(u.played, 0) = ?", toInt(*iq.Played))
	}
	if iq.Favorite {
		b.where("u.favorite = 1")
	}
}

// Items returns a page of movies or series and the cursor of the next page (nil at the end). The
// order is completed by the ID, so that ties keep a stable order from one page to the next.
func (q Q) Items(ctx context.Context, iq ItemQuery) ([]domain.ItemView, *Cursor, error) {
	b, err := itemsQuery(iq)
	if err != nil {
		return nil, nil, err
	}
	cards, err := q.cards(ctx, b)
	if err != nil {
		return nil, nil, err
	}
	if len(cards) <= iq.Limit {
		return cards, nil, nil
	}
	cards = cards[:iq.Limit]
	last := cards[len(cards)-1].Item
	next := &Cursor{ID: last.ID}
	switch iq.Sort {
	case domain.SortTitle:
		next.Text = last.SortTitle
	case domain.SortAdded:
		next.Num = float64(last.AddedAt.UnixMilli())
	case domain.SortReleased:
		next.Text = last.PremiereDate
		if next.Text == "" {
			next.Text = fmt.Sprintf("%04d", last.Year)
		}
	case domain.SortRating:
		next.Num = last.CommunityRating
	}
	return cards, next, nil
}

// itemsQuery builds the query of a page. It asks for one row more than the page, to know whether
// there is a next one.
func itemsQuery(iq ItemQuery) (*query, error) {
	spec, ok := sorts[iq.Sort]
	if !ok {
		return nil, fmt.Errorf("store: unknown sort order %q", iq.Sort)
	}
	desc := spec.desc != iq.Reverse
	b := cardQuery(iq.Viewer)
	itemFilters(b, iq)
	if iq.After != nil {
		op := ">"
		if desc {
			op = "<"
		}
		var v any = iq.After.Text
		if spec.num {
			v = iq.After.Num
		}
		b.where("("+spec.expr+", i.id) "+op+" (?, ?)", v, iq.After.ID)
	}
	b.flushWhere()
	dir := ""
	if desc {
		dir = " DESC"
	}
	b.add(" ORDER BY "+spec.expr+dir+", i.id"+dir+" LIMIT ?", iq.Limit+1)
	return b, nil
}

// CountItems counts the items of a query, all pages together.
func (q Q) CountItems(ctx context.Context, iq ItemQuery) (int, error) {
	var b query
	b.add("SELECT COUNT(*) FROM items i LEFT JOIN user_data u ON u.item_id = i.id AND u.profile_id = ?", iq.Viewer.ProfileID)
	itemFilters(&b, iq)
	viewerFilter(&b, iq.Viewer)
	b.flushWhere()
	var n int
	err := q.db.QueryRowContext(ctx, b.String(), b.args...).Scan(&n)
	return n, err
}

// View reads an item as a profile sees it, whether it has a file present or not. ErrNotFound if the
// item does not exist or the profile may not see it.
func (q Q) View(ctx context.Context, v domain.Viewer, itemID domain.ID) (domain.ItemView, error) {
	b := cardQuery(v)
	b.where("i.id = ?", itemID).flushWhere()
	cards, err := q.cards(ctx, b)
	if err != nil {
		return domain.ItemView{}, err
	}
	if len(cards) == 0 {
		return domain.ItemView{}, ErrNotFound
	}
	return cards[0], nil
}

// Seasons lists the seasons of a series that have at least one episode present.
func (q Q) Seasons(ctx context.Context, v domain.Viewer, seriesID domain.ID) ([]domain.ItemView, error) {
	b := cardQuery(v)
	b.where("i.kind = 'season'").where("s.series_id = ?", seriesID).where(presentCond(domain.ItemSeason)).flushWhere()
	b.add(" ORDER BY s.number")
	return q.cards(ctx, b)
}

// Episodes lists the episodes present in a series, or in one of its seasons if seasonID is not nil,
// in airing order.
func (q Q) Episodes(ctx context.Context, v domain.Viewer, seriesID domain.ID, seasonID *domain.ID) ([]domain.ItemView, error) {
	b := cardQuery(v)
	b.where("i.kind = 'episode'").where("e.series_id = ?", seriesID).where(presentCond(domain.ItemEpisode))
	if seasonID != nil {
		b.where("e.season_id = ?", *seasonID)
	}
	b.flushWhere()
	b.add(" ORDER BY e.season_number, e.number, i.sort_title")
	return q.cards(ctx, b)
}

// Search looks up movies, series, episodes, artists, albums, tracks, book series, books and photo
// albums that are present, by title (not photos: "IMG_1234"), ignoring accents and case. Each word
// may be just the beginning of a word. Episodes and tracks come after everything else, then results
// are ordered by relevance.
func (q Q) Search(ctx context.Context, v domain.Viewer, text string, limit int) ([]domain.ItemView, error) {
	match := ftsQuery(text)
	if match == "" {
		return nil, nil
	}
	b := cardQuery(v)
	b.add(" JOIN items_fts ON items_fts.rowid = i.rowid")
	b.where("items_fts MATCH ?", match).where("i.kind IN ('movie', 'series', 'episode', 'artist', 'album', 'track', 'book_series', 'book', 'photo_album')")
	b.where(presentAny)
	b.flushWhere()
	b.add(" ORDER BY i.kind IN ('episode', 'track'), bm25(items_fts), i.sort_title LIMIT ?", limit)
	return q.cards(ctx, b)
}

// maxSearchTerms caps the number of words in a search.
const maxSearchTerms = 8

// ftsQuery turns free text into an FTS5 query: each word (letters and digits) becomes a quoted
// prefix, and all are required. FTS5 syntax typed by the user (NEAR, OR, "-"...) is therefore never
// interpreted.
func ftsQuery(text string) string {
	words := strings.FieldsFunc(text, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	if len(words) > maxSearchTerms {
		words = words[:maxSearchTerms]
	}
	for i, w := range words {
		words[i] = `"` + w + `"*`
	}
	return strings.Join(words, " ")
}

// Genres lists the genres of the movies, series, albums and books a profile can see in a library
// (all of them if libraryID is nil).
func (q Q) Genres(ctx context.Context, v domain.Viewer, libraryID *domain.ID) ([]domain.GenreCount, error) {
	var b query
	b.add("SELECT g.genre, COUNT(*) FROM item_genres g JOIN items i ON i.id = g.item_id")
	b.where("i.kind IN ('movie', 'series', 'album', 'book')")
	viewerFilter(&b, v)
	if libraryID != nil {
		b.where("i.library_id = ?", *libraryID)
	}
	b.flushWhere().add(" GROUP BY g.genre ORDER BY g.genre COLLATE NOCASE")
	rows, err := q.db.QueryContext(ctx, b.String(), b.args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []domain.GenreCount
	for rows.Next() {
		var g domain.GenreCount
		if err := rows.Scan(&g.Name, &g.Count); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// User data.

// SetPlayed marks items as played or unplayed for a profile. The resume point goes back to zero,
// and the last played date is only set when marking as played.
func (q Q) SetPlayed(ctx context.Context, profileID domain.ID, itemIDs []domain.ID, played bool, now time.Time) error {
	last := sql.NullInt64{}
	if played {
		last = sql.NullInt64{Int64: toMillis(now), Valid: true}
	}
	for _, id := range itemIDs {
		if err := q.q.UpsertPlayed(ctx, sqlc.UpsertPlayedParams{
			ProfileID: profileID, ItemID: id, Played: toInt(played), LastPlayedAt: last, UpdatedAt: toMillis(now),
		}); err != nil {
			return err
		}
	}
	return nil
}

// SetFavorite adds an item to a profile's favorites or removes it.
func (q Q) SetFavorite(ctx context.Context, profileID, itemID domain.ID, favorite bool, now time.Time) error {
	return q.q.UpsertFavorite(ctx, sqlc.UpsertFavoriteParams{
		ProfileID: profileID, ItemID: itemID, Favorite: toInt(favorite), UpdatedAt: toMillis(now),
	})
}

// SeasonIDs lists the seasons of a series, in order.
func (q Q) SeasonIDs(ctx context.Context, seriesID domain.ID) ([]domain.ID, error) {
	return q.q.ListSeasonIDs(ctx, seriesID)
}

// EpisodeIDs lists the episodes of a series or a season (present or not).
func (q Q) EpisodeIDs(ctx context.Context, parent domain.Item) ([]domain.ID, error) {
	switch parent.Kind {
	case domain.ItemSeries:
		return q.q.ListSeriesEpisodeIDs(ctx, parent.ID)
	case domain.ItemSeason:
		return q.q.ListSeasonEpisodeIDs(ctx, parent.ID)
	case domain.ItemMovie, domain.ItemEpisode, domain.ItemArtist, domain.ItemAlbum, domain.ItemTrack,
		domain.ItemBookSeries, domain.ItemBook, domain.ItemPhotoAlbum, domain.ItemPhoto:
	}
	return nil, fmt.Errorf("store: %s has no episodes", parent.Kind)
}

// Tracks lists the tracks present in an album (in disc order), or in all the albums of an artist
// (album by album, oldest first).
func (q Q) Tracks(ctx context.Context, v domain.Viewer, parent domain.Item) ([]domain.ItemView, error) {
	b := cardQuery(v)
	b.where("i.kind = 'track'").where(presentCond(domain.ItemTrack))
	switch parent.Kind {
	case domain.ItemAlbum:
		b.where("t.album_id = ?", parent.ID).flushWhere()
		b.add(" ORDER BY t.disc, t.number, i.sort_title")
	case domain.ItemArtist:
		b.where("t.artist_id = ?", parent.ID).flushWhere()
		b.add(" ORDER BY tal.year, tal.sort_title, t.disc, t.number, i.sort_title")
	case domain.ItemMovie, domain.ItemSeries, domain.ItemSeason, domain.ItemEpisode, domain.ItemTrack,
		domain.ItemBookSeries, domain.ItemBook, domain.ItemPhotoAlbum, domain.ItemPhoto:
		return nil, fmt.Errorf("store: %s has no tracks", parent.Kind)
	}
	return q.cards(ctx, b)
}
