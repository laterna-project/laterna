package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/store/sqlc"
)

// Requests for movies, series, music and books, and where they land (docs/design/requests.md).

// Destinations.

// CreateRequestDestination stores a destination. ErrDuplicate if its name is taken.
func (q Q) CreateRequestDestination(ctx context.Context, d domain.RequestDestination) error {
	return translate(q.q.InsertRequestDestination(ctx, sqlc.InsertRequestDestinationParams{
		ID: d.ID, Name: d.Name, NameKey: NameKey(d.Name), Kind: string(d.Kind), LibraryID: d.LibraryID,
		RootFolder: d.RootFolder, QualityProfileID: int64(d.QualityProfileID), QualityProfileName: d.QualityProfileName,
		SeriesType: string(d.SeriesType), MetadataProfileID: int64(d.MetadataProfileID), MetadataProfileName: d.MetadataProfileName,
		CreatedAt: toMillis(d.CreatedAt), UpdatedAt: toMillis(d.UpdatedAt),
	}))
}

// UpdateRequestDestination rewrites a destination (not its kind). ErrDuplicate if its name is
// taken.
func (q Q) UpdateRequestDestination(ctx context.Context, d domain.RequestDestination) error {
	return translate(q.q.UpdateRequestDestination(ctx, sqlc.UpdateRequestDestinationParams{
		Name: d.Name, NameKey: NameKey(d.Name), LibraryID: d.LibraryID, RootFolder: d.RootFolder,
		QualityProfileID: int64(d.QualityProfileID), QualityProfileName: d.QualityProfileName,
		SeriesType: string(d.SeriesType), MetadataProfileID: int64(d.MetadataProfileID),
		MetadataProfileName: d.MetadataProfileName, UpdatedAt: toMillis(d.UpdatedAt), ID: d.ID,
	}))
}

// DeleteRequestDestination deletes a destination; the requests into it keep no destination.
func (q Q) DeleteRequestDestination(ctx context.Context, id domain.ID) error {
	return q.q.DeleteRequestDestination(ctx, id)
}

// RequestDestinations lists the destinations by kind, then name.
func (q Q) RequestDestinations(ctx context.Context) ([]domain.RequestDestination, error) {
	rows, err := q.q.ListRequestDestinations(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]domain.RequestDestination, len(rows))
	for i, r := range rows {
		out[i] = domain.RequestDestination{
			ID: r.ID, Name: r.Name, Kind: domain.RequestKind(r.Kind), LibraryID: r.LibraryID, LibraryName: r.LibraryName,
			RootFolder: r.RootFolder, QualityProfileID: int(r.QualityProfileID), QualityProfileName: r.QualityProfileName,
			SeriesType: domain.SeriesType(r.SeriesType), MetadataProfileID: int(r.MetadataProfileID),
			MetadataProfileName: r.MetadataProfileName, CreatedAt: fromMillis(r.CreatedAt), UpdatedAt: fromMillis(r.UpdatedAt),
		}
	}
	return out, nil
}

// RequestDestination reads a destination; sql.ErrNoRows if there is none.
func (q Q) RequestDestination(ctx context.Context, id domain.ID) (domain.RequestDestination, error) {
	all, err := q.RequestDestinations(ctx)
	if err != nil {
		return domain.RequestDestination{}, err
	}
	for _, d := range all {
		if d.ID == id {
			return d, nil
		}
	}
	return domain.RequestDestination{}, sql.ErrNoRows
}

// Requests.

// CreateRequest stores a request. ErrDuplicate if a request for the same title is already open.
func (q Q) CreateRequest(ctx context.Context, r domain.MediaRequest) error {
	seasons, err := json.Marshal(seasonNumbers(r.SeasonNumbers))
	if err != nil {
		return err
	}
	return translate(q.q.InsertRequest(ctx, sqlc.InsertRequestParams{
		ID: r.ID, Kind: string(r.Kind), ExternalID: r.ExternalID, ExternalKey: r.ExternalKey, Title: r.Title,
		Subtitle: r.Subtitle, Year: int64(r.Year), Poster: r.Poster,
		Status: string(r.Status), Seasons: string(r.Seasons), SeasonNumbers: string(seasons), DestinationID: destinationID(r),
		AccountID: r.AccountID, ProfileID: r.ProfileID, CreatedAt: toMillis(r.CreatedAt), UpdatedAt: toMillis(r.UpdatedAt),
		DecidedAt: nullMillis(r.DecidedAt), DecidedBy: r.DecidedBy,
	}))
}

// UpdateRequest rewrites what changes in a request after it was made. ErrDuplicate if it opens a
// request for a title that already has an open one.
func (q Q) UpdateRequest(ctx context.Context, r domain.MediaRequest) error {
	seasons, err := json.Marshal(seasonNumbers(r.SeasonNumbers))
	if err != nil {
		return err
	}
	var failure []byte
	if r.Error != nil {
		if failure, err = json.Marshal(r.Error); err != nil {
			return err
		}
	}
	return translate(q.q.UpdateRequest(ctx, sqlc.UpdateRequestParams{
		Status: string(r.Status), Seasons: string(r.Seasons), SeasonNumbers: string(seasons), DestinationID: destinationID(r),
		UpdatedAt: toMillis(r.UpdatedAt), DecidedAt: nullMillis(r.DecidedAt), DecidedBy: r.DecidedBy,
		DeclineReason: r.DeclineReason, Error: string(failure), ArrID: int64(r.ArrID), Progress: r.Progress, ItemID: r.ItemID,
		EpisodesAvailable: int64(r.EpisodesAvailable), EpisodesWanted: int64(r.EpisodesWanted),
		AvailableAt: nullMillis(r.AvailableAt), ID: r.ID,
	}))
}

func seasonNumbers(n []int) []int {
	if n == nil {
		return []int{}
	}
	return n
}

func destinationID(r domain.MediaRequest) *domain.ID {
	if r.Destination == nil {
		return nil
	}
	return &r.Destination.ID
}

// RequestPosters lists the poster addresses the requests keep.
func (q Q) RequestPosters(ctx context.Context) ([]string, error) { return q.q.RequestPosters(ctx) }

// DeleteRequest forgets a request.
func (q Q) DeleteRequest(ctx context.Context, id domain.ID) error { return q.q.DeleteRequest(ctx, id) }

// RequestCursor is the creation time and ID of the last request of a page (zero for the first
// page).
type RequestCursor struct {
	At time.Time
	ID domain.ID
}

// RequestQuery selects requests. Zero fields select everything.
type RequestQuery struct {
	ProfileID *domain.ID
	Statuses  []domain.RequestStatus
	IDs       []domain.ID
	After     RequestCursor
	// Limit is 0 for no limit.
	Limit int
}

// Requests lists requests, newest first, with who made them and their destination.
func (q Q) Requests(ctx context.Context, rq RequestQuery) ([]domain.MediaRequest, error) {
	var b query
	b.add(`SELECT r.id, r.kind, r.external_id, r.external_key, r.title, r.subtitle, r.year, r.poster, r.status, r.seasons,
		r.season_numbers,
		r.account_id, a.username, r.profile_id, p.name, r.created_at, r.updated_at, r.decided_at, r.decided_by,
		r.decline_reason, r.error, r.arr_id, r.progress, r.item_id, r.episodes_available, r.episodes_wanted,
		r.available_at, d.id, d.name, d.kind, d.library_id, l.name, d.root_folder, d.quality_profile_id,
		d.quality_profile_name, d.series_type, d.metadata_profile_id, d.metadata_profile_name, d.created_at, d.updated_at
		FROM requests r
		JOIN accounts a ON a.id = r.account_id
		JOIN profiles p ON p.id = r.profile_id
		LEFT JOIN request_destinations d ON d.id = r.destination_id
		LEFT JOIN libraries l ON l.id = d.library_id`)
	if rq.ProfileID != nil {
		b.where("r.profile_id = ?", *rq.ProfileID)
	}
	if len(rq.Statuses) > 0 {
		args := make([]any, len(rq.Statuses))
		for i, s := range rq.Statuses {
			args[i] = string(s)
		}
		b.where("r.status IN ("+placeholders(len(args))+")", args...)
	}
	if len(rq.IDs) > 0 {
		args := make([]any, len(rq.IDs))
		for i, id := range rq.IDs {
			args[i] = id
		}
		b.where("r.id IN ("+placeholders(len(args))+")", args...)
	}
	if !rq.After.At.IsZero() {
		b.where("(r.created_at, r.id) < (?, ?)", toMillis(rq.After.At), rq.After.ID)
	}
	b.flushWhere().add(" ORDER BY r.created_at DESC, r.id DESC")
	if rq.Limit > 0 {
		b.add(" LIMIT ?", rq.Limit)
	}
	rows, err := q.db.QueryContext(ctx, b.String(), b.args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []domain.MediaRequest
	for rows.Next() {
		r, err := scanRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func scanRequest(rows *sql.Rows) (domain.MediaRequest, error) {
	var (
		r                                         domain.MediaRequest
		kind, status, seasons, numbers, failure   string
		year, arrID, available, wanted            int64
		createdAt, updatedAt                      int64
		decidedAt, availableAt                    sql.NullInt64
		destID, destLibrary                       *domain.ID
		destName, destKind, destLibName, destRoot sql.NullString
		destProfileName, destType, destMetaName   sql.NullString
		destProfile, destMeta                     sql.NullInt64
		destCreated, destUpdated                  sql.NullInt64
	)
	if err := rows.Scan(&r.ID, &kind, &r.ExternalID, &r.ExternalKey, &r.Title, &r.Subtitle, &year, &r.Poster, &status,
		&seasons, &numbers,
		&r.AccountID, &r.Username, &r.ProfileID, &r.ProfileName, &createdAt, &updatedAt, &decidedAt, &r.DecidedBy,
		&r.DeclineReason, &failure, &arrID, &r.Progress, &r.ItemID, &available, &wanted,
		&availableAt, &destID, &destName, &destKind, &destLibrary, &destLibName, &destRoot, &destProfile,
		&destProfileName, &destType, &destMeta, &destMetaName, &destCreated, &destUpdated); err != nil {
		return r, err
	}
	r.Kind, r.Status, r.Seasons = domain.RequestKind(kind), domain.RequestStatus(status), domain.RequestSeasons(seasons)
	r.Year, r.ArrID, r.EpisodesAvailable, r.EpisodesWanted = int(year), int(arrID), int(available), int(wanted)
	r.CreatedAt, r.UpdatedAt = fromMillis(createdAt), fromMillis(updatedAt)
	r.DecidedAt, r.AvailableAt = optTime(decidedAt), optTime(availableAt)
	if err := json.Unmarshal([]byte(numbers), &r.SeasonNumbers); err != nil {
		return r, fmt.Errorf("request %s: seasons: %w", r.ID, err)
	}
	if failure != "" {
		r.Error = new(domain.Text)
		if err := json.Unmarshal([]byte(failure), r.Error); err != nil {
			return r, fmt.Errorf("request %s: error: %w", r.ID, err)
		}
	}
	if destID != nil && destLibrary != nil {
		r.Destination = &domain.RequestDestination{
			ID: *destID, Name: destName.String, Kind: domain.RequestKind(destKind.String), LibraryID: *destLibrary,
			LibraryName: destLibName.String, RootFolder: destRoot.String, QualityProfileID: int(destProfile.Int64),
			QualityProfileName: destProfileName.String, SeriesType: domain.SeriesType(destType.String),
			MetadataProfileID: int(destMeta.Int64), MetadataProfileName: destMetaName.String,
			CreatedAt: fromMillis(destCreated.Int64), UpdatedAt: fromMillis(destUpdated.Int64),
		}
	}
	return r, nil
}

// Request reads a request; sql.ErrNoRows if there is none.
func (q Q) Request(ctx context.Context, id domain.ID) (domain.MediaRequest, error) {
	list, err := q.Requests(ctx, RequestQuery{IDs: []domain.ID{id}})
	if err != nil {
		return domain.MediaRequest{}, err
	}
	if len(list) == 0 {
		return domain.MediaRequest{}, sql.ErrNoRows
	}
	return list[0], nil
}

// CountRequestsSince counts the requests an account made since a time, whatever became of them.
func (q Q) CountRequestsSince(ctx context.Context, accountID domain.ID, since time.Time) (int, error) {
	n, err := q.q.CountRequestsSince(ctx, sqlc.CountRequestsSinceParams{AccountID: accountID, CreatedAt: toMillis(since)})
	return int(n), err
}

// CountPendingRequests counts the requests waiting for an administrator.
func (q Q) CountPendingRequests(ctx context.Context) (int, error) {
	n, err := q.q.CountPendingRequests(ctx)
	return int(n), err
}

// requestKey is the external ID of a request as text: its key, or its number for a series or a
// movie (domain.MediaRequest.Key).
const requestKey = "(CASE external_key WHEN '' THEN CAST(external_id AS TEXT) ELSE external_key END)"

// OpenRequests returns the open request for each of these titles (domain.MediaRequest.Key) that
// has one.
func (q Q) OpenRequests(ctx context.Context, kind domain.RequestKind, keys []string) (map[string]domain.ID, error) {
	out := map[string]domain.ID{}
	if len(keys) == 0 {
		return out, nil
	}
	var b query
	args := []any{string(kind)}
	for _, k := range keys {
		args = append(args, k)
	}
	b.add(`SELECT `+requestKey+`, id FROM requests
		WHERE kind = ? AND status IN ('pending', 'approved', 'downloading') AND `+requestKey+` IN (`+placeholders(len(keys))+")", args...)
	rows, err := q.db.QueryContext(ctx, b.String(), b.args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			key string
			id  domain.ID
		)
		if err := rows.Scan(&key, &id); err != nil {
			return nil, err
		}
		out[key] = id
	}
	return out, rows.Err()
}

// RequestsOnTheirWay lists the approved and downloading requests, and the series available since
// a time that still wait for episodes, oldest first.
func (q Q) RequestsOnTheirWay(ctx context.Context, availableSince time.Time) ([]domain.ID, error) {
	return q.q.RequestsOnTheirWay(ctx, sql.NullInt64{Int64: toMillis(availableSince), Valid: true})
}

// DeclinedRequest is a request declined because its destination went away.
type DeclinedRequest struct {
	ID        domain.ID
	ProfileID domain.ID
}

// DeclinePendingRequestsTo declines the pending requests into a destination.
func (q Q) DeclinePendingRequestsTo(ctx context.Context, destinationID domain.ID, now time.Time) ([]DeclinedRequest, error) {
	rows, err := q.q.DeclinePendingRequestsTo(ctx, sqlc.DeclinePendingRequestsToParams{Now: toMillis(now), DestinationID: &destinationID})
	if err != nil {
		return nil, err
	}
	out := make([]DeclinedRequest, len(rows))
	for i, r := range rows {
		out[i] = DeclinedRequest{ID: r.ID, ProfileID: r.ProfileID}
	}
	return out, nil
}

// externalPresent keeps movies with a file present, and series, albums and artists with an
// episode or a track present.
const externalPresent = `((i.kind = 'movie' AND i.present = 1)
	OR (i.kind = 'series' AND EXISTS (SELECT 1 FROM episodes e JOIN items ei ON ei.id = e.item_id
		WHERE e.series_id = i.id AND ei.present = 1))
	OR (i.kind = 'album' AND EXISTS (SELECT 1 FROM tracks t JOIN items ti ON ti.id = t.item_id
		WHERE t.album_id = i.id AND ti.present = 1))
	OR (i.kind = 'artist' AND EXISTS (SELECT 1 FROM tracks t JOIN items ti ON ti.id = t.item_id
		WHERE t.artist_id = i.id AND ti.present = 1)))`

// ExternalItem is a movie, a series, an album or an artist of the catalog that has files, found by
// its external ID.
type ExternalItem struct {
	ItemID    domain.ID
	LibraryID domain.ID
}

// ItemsWithExternalIDs returns, for each of these external IDs (provider "tvdb", "tmdb",
// "musicbrainz_artist" or "musicbrainz_releasegroup", as the catalog names them), the items with
// files that carry it.
func (q Q) ItemsWithExternalIDs(ctx context.Context, provider string, values []string) (map[string][]ExternalItem, error) {
	out := map[string][]ExternalItem{}
	if len(values) == 0 {
		return out, nil
	}
	if len(values) == 1 {
		rows, err := q.q.ItemWithExternalID(ctx, sqlc.ItemWithExternalIDParams{Provider: provider, Value: values[0]})
		for _, r := range rows {
			out[values[0]] = append(out[values[0]], ExternalItem{ItemID: r.ItemID, LibraryID: r.LibraryID})
		}
		return out, err
	}
	var b query
	args := []any{provider}
	for _, v := range values {
		args = append(args, v)
	}
	b.add(`SELECT p.value, p.item_id, i.library_id FROM provider_ids p JOIN items i ON i.id = p.item_id
		WHERE p.provider = ? AND p.value IN (`+placeholders(len(values))+`) AND `+externalPresent, args...)
	rows, err := q.db.QueryContext(ctx, b.String(), b.args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			value string
			it    ExternalItem
		)
		if err := rows.Scan(&value, &it.ItemID, &it.LibraryID); err != nil {
			return nil, err
		}
		out[value] = append(out[value], it)
	}
	return out, rows.Err()
}

// CatalogBook is a book with a file present, as requests look for it: its title, ISBN and authors.
type CatalogBook struct {
	ItemID    domain.ID
	LibraryID domain.ID
	Title     string
	ISBN      string
	Authors   []string
}

// authorSeparator joins the authors of a book in one column (the unit separator, never in a name).
const authorSeparator = "\x1f"

// CatalogBooks lists the books with a file present, with their authors. Books have no external ID
// LazyLibrarian shares: requests find them by ISBN, or by title and author.
func (q Q) CatalogBooks(ctx context.Context) ([]CatalogBook, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT i.id, i.library_id, i.title, b.isbn,
		coalesce((SELECT group_concat(pe.name, char(31)) FROM item_people c JOIN people pe ON pe.id = c.person_id
			WHERE c.item_id = i.id AND c.role = 'writer'), '')
		FROM items i JOIN books b ON b.item_id = i.id
		WHERE i.kind = 'book' AND i.present = 1`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []CatalogBook
	for rows.Next() {
		var (
			b       CatalogBook
			authors string
		)
		if err := rows.Scan(&b.ItemID, &b.LibraryID, &b.Title, &b.ISBN, &authors); err != nil {
			return nil, err
		}
		if authors != "" {
			b.Authors = strings.Split(authors, authorSeparator)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// SeriesEpisodesPresent counts the episodes of a series that have files, specials excluded.
func (q Q) SeriesEpisodesPresent(ctx context.Context, seriesID domain.ID) (int, error) {
	var n int64
	err := q.db.QueryRowContext(ctx, `SELECT count(*) FROM episodes e JOIN items i ON i.id = e.item_id
		WHERE e.series_id = ? AND e.season_number > 0 AND i.present = 1`, seriesID).Scan(&n)
	return int(n), err
}
