package app

import (
	"context"
	"slices"
	"time"

	"github.com/laterna-project/laterna/internal/arr"
	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/store"
)

// Choosing the albums of a requested artist (docs/design/requests.md). Lidarr only knows the
// albums of an artist it has: the artist is added with nothing monitored when the request is
// approved, the request then waits (RequestChoosing) for its profile or an administrator to pick
// albums from Lidarr's list, and only those are monitored and searched.

const (
	// maxChosenAlbums bounds one choice.
	maxChosenAlbums = 500
	// discographyWait is how long the hand-over waits for Lidarr to list the albums of an artist it
	// just added, before telling the profile it can choose.
	discographyWait  = time.Minute
	discographyEvery = 2 * time.Second
)

// choosingAlbums says whether the albums of a request are chosen after its approval.
func choosingAlbums(r domain.MediaRequest) bool {
	return r.Kind == domain.RequestArtist && r.Seasons == domain.SeasonsChosen
}

// awaitDiscography waits until Lidarr lists albums for an artist, or gives up after a while: the
// list is read again when someone opens it.
func (a *App) awaitDiscography(ctx context.Context, artistID int) {
	client, _, err := a.requestClient(ctx, domain.RequestArtist)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, discographyWait)
	defer cancel()
	for {
		if albums, err := client.ArtistAlbums(ctx, artistID); err != nil || len(albums) > 0 {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(discographyEvery):
		}
	}
}

// albumRequest reads a request whose albums can be listed: an artist on Lidarr, for its profile or
// an administrator.
func (a *App) albumRequest(ctx context.Context, p domain.Principal, id domain.ID) (domain.MediaRequest, error) {
	r, err := a.store.Read().Request(ctx, id)
	if store.IsNotFound(err) || (err == nil && !p.CanAdminister() && (p.Profile == nil || r.ProfileID != p.Profile.ID)) {
		return domain.MediaRequest{}, domain.NotFound("media_request.not_found")
	}
	if err != nil {
		return domain.MediaRequest{}, err
	}
	if r.Kind != domain.RequestArtist || r.ArrID == 0 {
		return domain.MediaRequest{}, domain.Precondition("media_request.not_choosing")
	}
	return r, nil
}

// RequestAlbums lists the albums of the artist of a request, as Lidarr has them (those its
// metadata profile keeps), oldest first. The list is empty while Lidarr reads the discography of
// an artist it just added.
func (a *App) RequestAlbums(ctx context.Context, p domain.Principal, id domain.ID) ([]domain.ArtistAlbum, error) {
	r, err := a.albumRequest(ctx, p, id)
	if err != nil {
		return nil, err
	}
	client, _, err := a.requestClient(ctx, r.Kind)
	if err != nil {
		return nil, err
	}
	albums, err := client.ArtistAlbums(ctx, r.ArrID)
	if err != nil {
		return nil, a.instanceError(ctx, p, sourceName(r.Kind), err)
	}
	out := make([]domain.ArtistAlbum, 0, len(albums))
	for _, al := range albums {
		a.rememberPoster(al.Poster)
		out = append(out, requestAlbum(al))
	}
	return out, nil
}

func requestAlbum(al arr.Album) domain.ArtistAlbum {
	return domain.ArtistAlbum{
		Key: al.MBID, Title: al.Title, Year: al.Year, Type: al.Type, SecondaryTypes: al.SecondaryTypes,
		Tracks: al.Tracks, Monitored: al.Monitored, Available: al.Tracks > 0 && al.Files >= al.Tracks,
		Poster: al.Poster,
	}
}

// ChooseRequestAlbums says which albums a request asks for: Lidarr monitors and searches them (and
// the next ones as they come out, if future is set), and the request goes on as approved. For the
// profile of the request or an administrator, while the request waits for that choice.
func (a *App) ChooseRequestAlbums(ctx context.Context, p domain.Principal, id domain.ID, keys []string, future bool) (domain.MediaRequest, error) {
	if p.Profile == nil {
		return domain.MediaRequest{}, domain.Precondition("profile.required")
	}
	r, err := a.albumRequest(ctx, p, id)
	if err != nil {
		return domain.MediaRequest{}, err
	}
	if r.Status != domain.RequestChoosing {
		return domain.MediaRequest{}, domain.Precondition("media_request.not_choosing")
	}
	keys = slices.Compact(slices.Sorted(slices.Values(keys)))
	if len(keys) == 0 || len(keys) > maxChosenAlbums {
		return domain.MediaRequest{}, domain.Invalid("media_request.albums_required")
	}
	client, _, err := a.requestClient(ctx, r.Kind)
	if err != nil {
		return domain.MediaRequest{}, err
	}
	albums, err := client.ArtistAlbums(ctx, r.ArrID)
	if err != nil {
		return domain.MediaRequest{}, a.instanceError(ctx, p, sourceName(r.Kind), err)
	}
	var (
		ids    []int
		titles []string
	)
	for _, al := range albums { // oldest first: the titles are kept in that order
		if _, wanted := slices.BinarySearch(keys, al.MBID); wanted {
			ids, titles = append(ids, al.ArrID), append(titles, al.Title)
		}
	}
	if len(ids) != len(keys) {
		return domain.MediaRequest{}, domain.Invalid("media_request.unknown_album")
	}
	if err := client.ChooseAlbums(ctx, r.ArrID, ids, future); err != nil {
		return domain.MediaRequest{}, a.instanceError(ctx, p, sourceName(r.Kind), err)
	}
	now := a.now()
	err = a.store.Write(ctx, func(q store.Q) error {
		cur, err := q.Request(ctx, id)
		if store.IsNotFound(err) {
			return domain.NotFound("media_request.not_found")
		}
		if err != nil {
			return err
		}
		if cur.Status != domain.RequestChoosing {
			return domain.Precondition("media_request.not_choosing")
		}
		cur.Status, cur.AlbumTitles, cur.UpdatedAt = domain.RequestApproved, titles, now
		if err := q.UpdateRequest(ctx, cur); err != nil {
			return err
		}
		r = cur
		return a.jobs.EnqueueAfter(ctx, q, jobRefreshRequests, "", priorityBackground, requestsAfterSubmit)
	})
	if err != nil {
		return domain.MediaRequest{}, err
	}
	a.jobs.Kick()
	a.log.InfoContext(ctx, "albums of a request chosen", "request", id, "title", r.Title, "albums", len(ids), "future", future)
	a.record(ctx, domain.Activity{
		Kind: domain.ActivityRequest, AccountID: &p.Account.ID, ProfileID: &p.Profile.ID,
		Text: domain.T("activity.request_albums_chosen", "profile", p.Profile.Name, "title", r.Title),
	})
	a.requestsChanged(r.ProfileID, r.ID)
	return requestFor(p, r), nil
}
