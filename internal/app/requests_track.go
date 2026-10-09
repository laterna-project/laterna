package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/laterna-project/laterna/internal/arr"
	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/jobs"
	"github.com/laterna-project/laterna/internal/media/images"
	"github.com/laterna-project/laterna/internal/metadata/download"
	"github.com/laterna-project/laterna/internal/store"
)

// What happens to a request once approved: handed to the instance, then followed until the catalog
// has it. Posters of search results and requests are served by the server.

// submitRequest hands an approved request to the instance (job request.submit): the title is added,
// monitored and searched, or monitored and searched if the instance already has it. A refusal
// fails the request at once; anything else is retried like any job, and fails it when the last
// attempt fails (requestFailed).
func (a *App) submitRequest(ctx context.Context, target string) error {
	id, err := domain.ParseID(target)
	if err != nil {
		return jobs.Permanent(err)
	}
	r, err := a.store.Read().Request(ctx, id)
	if store.IsNotFound(err) || (err == nil && r.Status != domain.RequestApproved) {
		return nil // deleted, declined or handed over in the meantime
	}
	if err != nil {
		return err
	}
	ak := requestArr(r.Kind)
	if r.Destination == nil {
		return a.failRequest(ctx, r, domain.T("error.media_request.destination_required"))
	}
	client, _, err := a.requestClient(ctx, r.Kind)
	if err != nil {
		return a.failRequest(ctx, r, domain.T("error.media_request.unavailable", "name", ak.Name()))
	}
	arrID, err := client.Add(ctx, r.ExternalID, arr.AddOptions{
		RootFolder: r.Destination.RootFolder, QualityProfileID: r.Destination.QualityProfileID,
		SeriesType: string(r.Destination.SeriesType), Seasons: string(r.Seasons), SeasonNumbers: r.SeasonNumbers,
	})
	var refused *arr.Error
	if errors.As(err, &refused) || errors.Is(err, arr.ErrUnauthorized) {
		return a.failRequest(ctx, r, arrProblem(ak, err))
	}
	if err != nil {
		return err
	}
	err = a.store.Write(ctx, func(q store.Q) error {
		cur, err := q.Request(ctx, id)
		if store.IsNotFound(err) || (err == nil && cur.Status != domain.RequestApproved) {
			return nil
		}
		if err != nil {
			return err
		}
		cur.ArrID, cur.UpdatedAt = arrID, a.now()
		if err := q.UpdateRequest(ctx, cur); err != nil {
			return err
		}
		return a.jobs.EnqueueAfter(ctx, q, jobRefreshRequests, "", priorityBackground, requestsAfterSubmit)
	})
	if err != nil {
		return err
	}
	a.jobs.Kick()
	a.log.InfoContext(ctx, "request handed to the instance", "request", id, "kind", ak, "title", r.Title, "arr_id", arrID)
	a.requestsChanged(r.ProfileID, r.ID)
	return nil
}

// requestFailed fails a request whose hand-over failed for good (jobs.OnFailure).
func (a *App) requestFailed(ctx context.Context, target string, cause error) {
	id, err := domain.ParseID(target)
	if err != nil {
		return
	}
	r, err := a.store.Read().Request(ctx, id)
	if err != nil || r.Status != domain.RequestApproved {
		return
	}
	if err := a.failRequest(ctx, r, arrProblem(requestArr(r.Kind), cause)); err != nil {
		a.log.WarnContext(ctx, "cannot mark the request failed", "request", id, "err", err)
	}
}

// failRequest marks an approved request failed, with the reason. The job ends there.
func (a *App) failRequest(ctx context.Context, r domain.MediaRequest, reason domain.Text) error {
	err := a.store.Write(ctx, func(q store.Q) error {
		cur, err := q.Request(ctx, r.ID)
		if store.IsNotFound(err) || (err == nil && cur.Status != domain.RequestApproved) {
			return nil
		}
		if err != nil {
			return err
		}
		cur.Status, cur.Error, cur.UpdatedAt = domain.RequestFailed, &reason, a.now()
		return q.UpdateRequest(ctx, cur)
	})
	if err != nil {
		return err
	}
	a.log.WarnContext(ctx, "request failed", "request", r.ID, "title", r.Title, "reason", reason.String())
	a.record(ctx, domain.Activity{
		Kind: domain.ActivityRequest, Warning: true, ProfileID: &r.ProfileID,
		Text: domain.T("activity.request_failed", "title", r.Title, "profile", r.ProfileName, []domain.Text{reason}),
	})
	a.requestsChanged(r.ProfileID, r.ID)
	return nil
}

// refreshRequests follows the requests on their way (job requests.refresh): what the instance is
// downloading, then whether the catalog has the title. A series is available from its first
// episode and keeps counting the episodes that arrive.
func (a *App) refreshRequests(ctx context.Context, _ string) error {
	read := a.store.Read()
	now := a.now()
	ids, err := read.RequestsOnTheirWay(ctx, now.Add(-requestEpisodesFor))
	if err != nil || len(ids) == 0 {
		return err
	}
	list, err := read.Requests(ctx, store.RequestQuery{IDs: ids})
	if err != nil {
		return err
	}
	byKind := map[domain.RequestKind][]domain.MediaRequest{}
	for _, r := range list {
		if r.ArrID > 0 {
			byKind[r.Kind] = append(byKind[r.Kind], r)
		}
	}
	var errs []error
	for kind, reqs := range byKind {
		if err := a.refreshKind(ctx, kind, reqs); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (a *App) refreshKind(ctx context.Context, kind domain.RequestKind, reqs []domain.MediaRequest) error {
	client, ak, err := a.requestClient(ctx, kind)
	if err != nil {
		return nil //nolint:nilerr // the integration is gone: nothing to follow
	}
	queue, err := client.Queue(ctx)
	if err != nil {
		return fmt.Errorf("%s queue: %w", ak, err)
	}
	downloading := map[int]arr.Download{}
	for _, d := range queue {
		downloading[d.ArrID] = d
	}
	read := a.store.Read()
	for _, r := range reqs {
		next := r
		items, err := read.ItemsWithExternalIDs(ctx, kind.ExternalProvider(), []int64{r.ExternalID})
		if err != nil {
			return err
		}
		item := catalogItem(items[r.ExternalID], r.Destination)
		if kind == domain.RequestSeries {
			t, err := client.Get(ctx, r.ArrID)
			var gone *arr.Error
			if errors.As(err, &gone) && gone.Status == http.StatusNotFound {
				continue // removed from the instance: the request stays where it was
			}
			if err != nil {
				return fmt.Errorf("%s series %d: %w", ak, r.ArrID, err)
			}
			next.EpisodesWanted = t.EpisodesWanted
			if item != nil {
				n, err := read.SeriesEpisodesPresent(ctx, *item)
				if err != nil {
					return err
				}
				next.EpisodesAvailable = n
				if t.EpisodesWanted > 0 {
					next.EpisodesAvailable = min(n, t.EpisodesWanted)
				}
			}
		}
		d, inQueue := downloading[r.ArrID]
		switch {
		case item != nil:
			next.Status, next.ItemID = domain.RequestAvailable, item
			next.Progress = 1
			if inQueue && d.Size > 0 {
				next.Progress = float64(d.Size-d.Left) / float64(d.Size)
			}
		case inQueue:
			next.Status = domain.RequestDownloading
			if d.Size > 0 {
				next.Progress = float64(d.Size-d.Left) / float64(d.Size)
			}
		case r.Status == domain.RequestDownloading:
			next.Progress = 1 // out of the queue: being imported
		}
		if sameProgress(r, next) {
			continue
		}
		becameAvailable := r.Status != domain.RequestAvailable && next.Status == domain.RequestAvailable
		if becameAvailable {
			at := a.now()
			next.AvailableAt = &at
		}
		next.UpdatedAt = a.now()
		if err := a.store.Write(ctx, func(q store.Q) error {
			cur, err := q.Request(ctx, r.ID)
			if store.IsNotFound(err) || (err == nil && cur.Status != r.Status) {
				return nil // changed in the meantime: the next round sees it
			}
			if err != nil {
				return err
			}
			return q.UpdateRequest(ctx, next)
		}); err != nil {
			return err
		}
		if becameAvailable {
			a.log.InfoContext(ctx, "request available", "request", r.ID, "title", r.Title)
			a.record(ctx, domain.Activity{
				Kind: domain.ActivityRequest, ProfileID: &r.ProfileID, ItemID: item,
				Text: domain.T("activity.request_available", "title", r.Title, "profile", r.ProfileName),
			})
		}
		a.requestsChanged(r.ProfileID, r.ID)
	}
	return nil
}

// catalogItem picks the item of a title, in the destination's library first.
func catalogItem(items []store.ExternalItem, dest *domain.RequestDestination) *domain.ID {
	if len(items) == 0 {
		return nil
	}
	for _, it := range items {
		if dest != nil && it.LibraryID == dest.LibraryID {
			return &it.ItemID
		}
	}
	return &items[0].ItemID
}

// sameProgress reports a request whose state did not change enough to be written again: progress
// moves by whole percents.
func sameProgress(r, next domain.MediaRequest) bool {
	sameItem := (r.ItemID == nil) == (next.ItemID == nil) && (r.ItemID == nil || *r.ItemID == *next.ItemID)
	return r.Status == next.Status && sameItem && int(r.Progress*100) == int(next.Progress*100) &&
		r.EpisodesAvailable == next.EpisodesAvailable && r.EpisodesWanted == next.EpisodesWanted
}

// followRequests asks for a round of requests.refresh after a delay, if requests are on their way.
func (a *App) followRequests(ctx context.Context, after time.Duration) {
	ids, err := a.store.Read().RequestsOnTheirWay(ctx, a.now().Add(-requestEpisodesFor))
	if err != nil || len(ids) == 0 {
		return
	}
	if err := a.store.Write(ctx, func(q store.Q) error {
		return a.jobs.EnqueueAfter(ctx, q, jobRefreshRequests, "", priorityBackground, after)
	}); err != nil {
		a.log.WarnContext(ctx, "requests: cannot schedule a refresh", "err", err)
		return
	}
	a.jobs.Kick()
}

// Posters.

const (
	// posterTTL is how long the poster of a search result can be served; maxPosters caps how many
	// are remembered.
	posterTTL  = time.Hour
	maxPosters = 10_000
	// posterWidth is the width posters are served at.
	posterWidth = 480
)

// posterURLs remembers the posters search results gave, by key.
type posterURLs struct {
	mu   sync.Mutex
	urls map[string]posterURL
}

type posterURL struct {
	url  string
	seen time.Time
}

// posterKey is the key of a poster: part of the hash of its address.
func posterKey(u string) string {
	sum := sha256.Sum256([]byte(u))
	return hex.EncodeToString(sum[:16])
}

// RequestPosterPath is the path, on the server, of a poster a search returned or a request keeps;
// empty without a poster.
func RequestPosterPath(poster string) string {
	if poster == "" {
		return ""
	}
	return "/requests/posters/" + posterKey(poster)
}

func (a *App) rememberPoster(u string) {
	if u == "" {
		return
	}
	now := a.now()
	a.posters.mu.Lock()
	defer a.posters.mu.Unlock()
	if a.posters.urls == nil {
		a.posters.urls = map[string]posterURL{}
	}
	if len(a.posters.urls) >= maxPosters {
		maps.DeleteFunc(a.posters.urls, func(_ string, p posterURL) bool { return now.Sub(p.seen) > posterTTL })
		if len(a.posters.urls) >= maxPosters {
			return
		}
	}
	a.posters.urls[posterKey(u)] = posterURL{url: u, seen: now}
}

// posterSource finds the address of a poster: a recent search result, or a request.
func (a *App) posterSource(ctx context.Context, key string) (string, bool, error) {
	a.posters.mu.Lock()
	p, ok := a.posters.urls[key]
	a.posters.mu.Unlock()
	if ok && a.now().Sub(p.seen) <= posterTTL {
		return p.url, true, nil
	}
	posters, err := a.store.Read().RequestPosters(ctx)
	if err != nil {
		return "", false, err
	}
	for _, u := range posters {
		if posterKey(u) == key {
			return u, true, nil
		}
	}
	return "", false, nil
}

// smallerPoster asks TMDB for a poster already scaled down rather than the original (several MB).
func smallerPoster(u string) string {
	parsed, err := url.Parse(u)
	if err != nil || parsed.Host != "image.tmdb.org" || !strings.Contains(parsed.Path, "/t/p/original/") {
		return u
	}
	parsed.Path = strings.Replace(parsed.Path, "/t/p/original/", "/t/p/w500/", 1)
	return parsed.String()
}

// RequestPoster returns the file of a poster, downloaded on first request, scaled down and kept in
// the cache.
func (a *App) RequestPoster(ctx context.Context, key string) (string, error) {
	if len(key) != 32 || strings.Trim(key, "0123456789abcdef") != "" {
		return "", domain.NotFound("media_request.poster_not_found")
	}
	path := filepath.Join(a.cacheDir, "requests", key[:2], key)
	if _, err := os.Stat(path); err == nil {
		return path, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	u, ok, err := a.posterSource(ctx, key)
	if err != nil {
		return "", err
	}
	if !ok || !a.Settings().DownloadImages {
		return "", domain.NotFound("media_request.poster_not_found")
	}
	original := path + ".original"
	if err := a.download.Image(ctx, smallerPoster(u), original); err != nil {
		if errors.Is(err, download.ErrUnavailable) {
			a.log.DebugContext(ctx, "poster unavailable", "url", u, "err", err)
			return "", domain.NotFound("media_request.poster_not_found")
		}
		return "", err
	}
	defer func() { _ = os.Remove(original) }()
	info, err := images.Analyze(original)
	if err != nil {
		a.log.DebugContext(ctx, "unreadable poster", "url", u, "err", err)
		return "", domain.NotFound("media_request.poster_not_found")
	}
	if info.Width > posterWidth {
		err = images.Resize(original, path, posterWidth)
	} else {
		err = os.Rename(original, path)
	}
	if err != nil {
		return "", err
	}
	return path, nil
}

// purgePosters forgets the cached posters no request keeps that were fetched more than a day ago.
func (a *App) purgePosters(ctx context.Context) error {
	root := filepath.Join(a.cacheDir, "requests")
	shards, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	posters, err := a.store.Read().RequestPosters(ctx)
	if err != nil {
		return err
	}
	keep := make(map[string]bool, len(posters))
	for _, u := range posters {
		keep[posterKey(u)] = true
	}
	limit := a.now().Add(-24 * time.Hour)
	for _, shard := range shards {
		files, err := os.ReadDir(filepath.Join(root, shard.Name()))
		if err != nil {
			continue
		}
		for _, f := range files {
			if keep[strings.TrimSuffix(f.Name(), ".original")] {
				continue
			}
			if info, err := f.Info(); err == nil && info.ModTime().Before(limit) {
				_ = os.Remove(filepath.Join(root, shard.Name(), f.Name()))
			}
		}
	}
	return nil
}
