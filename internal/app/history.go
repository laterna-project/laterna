package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"time"

	// Embedded time zones (450 KiB): statistics count days in the client's time zone, including on
	// Windows without Go installed or in an image without tzdata.
	_ "time/tzdata"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/stats"
	"github.com/laterna-project/laterna/internal/store"
)

// Playback history, statistics and yearly recap. Each playback of a movie, an episode or a track is
// kept, per profile, with the time actually watched. Statistics are computed from it on demand, in
// the client's time zone.

const (
	historyPageSize    = 50
	maxHistoryPageSize = 200
)

// HistoryEntry is a play from the history, with the item's card if it still exists and the profile
// can see it (nil otherwise: the title of the play stays).
type HistoryEntry struct {
	Play domain.Play
	View *domain.ItemView
}

// HistoryPage is a page of the history, newest play first.
type HistoryPage struct {
	Entries       []HistoryEntry
	NextPageToken string
}

type historyToken struct {
	At int64     `json:"at"`
	ID domain.ID `json:"id"`
}

// History lists the history of the picked profile.
func (a *App) History(ctx context.Context, p domain.Principal, pageToken string, pageSize int) (HistoryPage, error) {
	v, err := viewerOf(p)
	if err != nil {
		return HistoryPage{}, err
	}
	limit := historyPageSize
	if pageSize > 0 {
		limit = min(pageSize, maxHistoryPageSize)
	}
	var after store.PlayCursor
	if pageToken != "" {
		var t historyToken
		b, err := base64.RawURLEncoding.DecodeString(pageToken)
		if err == nil {
			err = json.Unmarshal(b, &t)
		}
		if err != nil {
			return HistoryPage{}, domain.Invalid("request.invalid_page_token")
		}
		after = store.PlayCursor{At: time.UnixMilli(t.At), ID: t.ID}
	}
	read := a.store.Read()
	plays, err := read.Plays(ctx, v.ProfileID, after, limit+1)
	if err != nil {
		return HistoryPage{}, err
	}
	var page HistoryPage
	if len(plays) > limit {
		plays = plays[:limit]
		last := plays[limit-1]
		b, _ := json.Marshal(historyToken{At: last.StartedAt.UnixMilli(), ID: last.ID}) // a number and an ID: cannot fail
		page.NextPageToken = base64.RawURLEncoding.EncodeToString(b)
	}
	var ids []domain.ID
	for _, pl := range plays {
		if pl.ItemID != nil {
			ids = append(ids, *pl.ItemID)
		}
	}
	views, err := read.ViewsByID(ctx, v, ids)
	if err != nil {
		return HistoryPage{}, err
	}
	for _, pl := range plays {
		e := HistoryEntry{Play: pl}
		if pl.ItemID != nil {
			if view, ok := views[*pl.ItemID]; ok {
				e.View = &view
			}
		}
		page.Entries = append(page.Entries, e)
	}
	return page, nil
}

// DeleteHistoryEntry deletes one play from the history of the picked profile.
func (a *App) DeleteHistoryEntry(ctx context.Context, p domain.Principal, id domain.ID) error {
	v, err := viewerOf(p)
	if err != nil {
		return err
	}
	defer a.tasteChanged(v.ProfileID)
	return a.store.Write(ctx, func(q store.Q) error {
		ok, err := q.DeletePlay(ctx, v.ProfileID, id)
		if err == nil && !ok {
			return domain.NotFound("history.not_found")
		}
		return err
	})
}

// ClearHistory wipes the whole history of the picked profile and returns the number of plays
// deleted.
func (a *App) ClearHistory(ctx context.Context, p domain.Principal) (int64, error) {
	v, err := viewerOf(p)
	if err != nil {
		return 0, err
	}
	defer a.tasteChanged(v.ProfileID)
	var n int64
	err = a.store.Write(ctx, func(q store.Q) error {
		var err error
		n, err = q.ClearPlays(ctx, v.ProfileID)
		return err
	})
	return n, err
}

// Stats sums up the history of the picked profile: calendar year year (the yearly recap), or all
// time (0). zone is the client's time zone ("Europe/Paris"), which sets days, hours and months;
// empty means the server's.
func (a *App) Stats(ctx context.Context, p domain.Principal, year int, zone string) (domain.Stats, error) {
	v, err := viewerOf(p)
	if err != nil {
		return domain.Stats{}, err
	}
	if year != 0 && (year < 1970 || year > 9999) {
		return domain.Stats{}, domain.Invalid("history.invalid_year", "year", year)
	}
	loc := time.Local
	if zone != "" {
		if loc, err = time.LoadLocation(zone); err != nil {
			return domain.Stats{}, domain.Invalid("history.unknown_time_zone", "time_zone", zone)
		}
	}
	read := a.store.Read()
	acc := stats.New(year, loc)
	from, to := stats.Bounds(year, loc)
	if err := read.EachPlay(ctx, v.ProfileID, from, to, acc.Add); err != nil {
		return domain.Stats{}, err
	}
	// Genres of the movies and of the series of the episodes watched.
	genres, err := read.GenresOf(ctx, acc.Owners())
	if err != nil {
		return domain.Stats{}, err
	}
	st := acc.Result(genres)
	if st.First, st.Last, err = read.FirstLastPlays(ctx, v.ProfileID, from, to); err != nil {
		return domain.Stats{}, err
	}
	if err := a.statImages(ctx, v, &st); err != nil {
		return domain.Stats{}, err
	}
	return st, nil
}

// statImages attaches to the rankings the images of the items that still exist, and the current
// name of an artist (the play keeps the artists of the track).
func (a *App) statImages(ctx context.Context, v domain.Viewer, st *domain.Stats) error {
	lists := [][]domain.StatEntry{st.TopSeries, st.TopMovies, st.TopArtists, st.TopTracks}
	var ids []domain.ID
	for _, list := range lists {
		for _, e := range list {
			if e.ID != nil {
				ids = append(ids, *e.ID)
			}
		}
	}
	views, err := a.store.Read().ViewsByID(ctx, v, ids)
	if err != nil {
		return err
	}
	for _, list := range lists {
		for i := range list {
			if list[i].ID == nil {
				continue
			}
			if view, ok := views[*list[i].ID]; ok {
				list[i].Images = view.Images
				if view.Item.Kind == domain.ItemArtist {
					list[i].Name = view.Item.Title
				}
			}
		}
	}
	return nil
}

// recordPlay keeps a play in the history if enough of it was watched.
func (a *App) recordPlay(ctx context.Context, pl domain.Play) {
	if !domain.CountsAsPlay(pl.Kind, pl.Watched, pl.Duration) {
		return
	}
	if err := a.store.Write(ctx, func(q store.Q) error { return q.AddPlay(ctx, pl) }); err != nil {
		a.log.WarnContext(ctx, "history: play not recorded", "item", pl.Title, "err", err)
		return
	}
	a.tasteChanged(pl.ProfileID)
}

// playOf prepares the play of a playback: the item as it is seen when playback opens.
func playOf(profile domain.ID, view domain.ItemView, duration time.Duration, now time.Time) domain.Play {
	id := view.Item.ID
	pl := domain.Play{
		ID: domain.NewID(), ProfileID: profile, ItemID: &id, Kind: view.Item.Kind, Title: view.Item.Title,
		StartedAt: now, Duration: duration,
	}
	switch {
	case view.Episode != nil:
		series := view.Episode.SeriesID
		pl.SeriesID, pl.Subtitle = &series, view.SeriesTitle
	case view.Track != nil:
		album, artist := view.Track.AlbumID, view.Track.ArtistID
		pl.AlbumID, pl.ArtistID, pl.Subtitle = &album, &artist, view.Track.Artists
		if pl.Subtitle == "" {
			pl.Subtitle = view.ArtistName
		}
	}
	return pl
}
