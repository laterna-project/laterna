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
	"github.com/laterna-project/laterna/internal/lazylibrarian"
	"github.com/laterna-project/laterna/internal/media/images"
	"github.com/laterna-project/laterna/internal/metadata/download"
	"github.com/laterna-project/laterna/internal/store"
)

// What happens to a request once approved: handed to the instance, then followed until the catalog
// has it. Posters of search results and requests are served by the server.

// submitRequest hands an approved request to its source (job request.submit): Sonarr, Radarr or
// Lidarr add the title monitored and search for it (or monitor and search it if they already have
// it), LazyLibrarian marks the ebook wanted and searches for it. A refusal fails the request at
// once; anything else is retried like any job, and fails it when the last attempt fails
// (requestFailed).
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
	if r.Destination == nil {
		return a.failRequest(ctx, r, domain.T("error.media_request.destination_required"))
	}
	arrID, err := a.handOver(ctx, r)
	var (
		expected *domain.Error
		refused  *arr.Error
		declined *lazylibrarian.Error
	)
	switch {
	case errors.As(err, &expected):
		return a.failRequest(ctx, r, expected.Text())
	case errors.As(err, &refused), errors.As(err, &declined),
		errors.Is(err, arr.ErrUnauthorized), errors.Is(err, lazylibrarian.ErrUnauthorized):
		return a.failRequest(ctx, r, integrationProblem(sourceName(r.Kind), err))
	case err != nil:
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
	a.log.InfoContext(ctx, "request handed to the instance", "request", id, "source", sourceName(r.Kind), "title", r.Title, "arr_id", arrID)
	a.requestsChanged(r.ProfileID, r.ID)
	return nil
}

// handOver gives a request to its source. It returns the title's ID there (series, movie, artist
// or album; 0 for a book: LazyLibrarian goes by the external key).
func (a *App) handOver(ctx context.Context, r domain.MediaRequest) (int, error) {
	d := r.Destination
	if r.Kind == domain.RequestBook {
		client, err := a.bookClient(ctx)
		if err != nil {
			return 0, err
		}
		return 0, client.Want(ctx, r.ExternalKey)
	}
	client, _, err := a.requestClient(ctx, r.Kind)
	if err != nil {
		return 0, err
	}
	switch r.Kind {
	case domain.RequestArtist, domain.RequestAlbum:
		opts := arr.MusicOptions{
			RootFolder: d.RootFolder, QualityProfileID: d.QualityProfileID, MetadataProfileID: d.MetadataProfileID,
			Albums: string(r.Seasons),
		}
		if r.Kind == domain.RequestArtist {
			return client.AddArtist(ctx, r.ExternalKey, opts)
		}
		id, _, err := client.AddAlbum(ctx, r.ExternalKey, opts)
		return id, err
	case domain.RequestSeries, domain.RequestMovie, domain.RequestMusic, domain.RequestBook:
	}
	return client.Add(ctx, r.ExternalID, arr.AddOptions{
		RootFolder: d.RootFolder, QualityProfileID: d.QualityProfileID,
		SeriesType: string(d.SeriesType), Seasons: string(r.Seasons), SeasonNumbers: r.SeasonNumbers,
	})
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
	if err := a.failRequest(ctx, r, integrationProblem(sourceName(r.Kind), cause)); err != nil {
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

// refreshRequests follows the requests on their way (job requests.refresh): what the source is
// downloading, then whether the catalog has the title. A series or an artist is available from its
// first episode or track, and keeps counting the episodes or tracks that arrive.
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
	byFamily := map[domain.RequestKind][]domain.MediaRequest{}
	for _, r := range list {
		// A book is followed by its key; the others once their source gave them an ID.
		if r.ArrID > 0 || r.Kind == domain.RequestBook {
			byFamily[r.Kind.Family()] = append(byFamily[r.Kind.Family()], r)
		}
	}
	var errs []error
	for family, reqs := range byFamily {
		var err error
		switch family {
		case domain.RequestBook:
			err = a.refreshBooks(ctx, reqs)
		case domain.RequestMusic:
			err = a.refreshMusic(ctx, reqs)
		default:
			err = a.refreshVideo(ctx, family, reqs)
		}
		if err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// progress is where a download stands: the share already there, from Size and Left.
func progress(size, left float64) float64 {
	if size <= 0 {
		return 0
	}
	return (size - left) / size
}

// nextStatus sets a request available when the catalog has its item, downloading while its
// source downloads it; out of the queue and not in the catalog yet, it is being imported.
func nextStatus(r domain.MediaRequest, next *domain.MediaRequest, item *domain.ID, inQueue bool, share float64) {
	switch {
	case item != nil:
		next.Status, next.ItemID = domain.RequestAvailable, item
		next.Progress = 1
		if inQueue {
			next.Progress = share
		}
	case inQueue:
		next.Status, next.Progress = domain.RequestDownloading, share
	case r.Status == domain.RequestDownloading:
		next.Progress = 1 // out of the queue: being imported
	}
}

func (a *App) refreshVideo(ctx context.Context, kind domain.RequestKind, reqs []domain.MediaRequest) error {
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
		items, err := read.ItemsWithExternalIDs(ctx, kind.ExternalProvider(), []string{r.Key()})
		if err != nil {
			return err
		}
		item := catalogItem(items[r.Key()], r.Destination)
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
		nextStatus(r, &next, item, inQueue, progress(d.Size, d.Left))
		if err := a.saveProgress(ctx, r, next, item); err != nil {
			return err
		}
	}
	return nil
}

// refreshMusic follows artist and album requests on Lidarr: its queue album by album, its track
// counts, then the catalog by MusicBrainz ID.
func (a *App) refreshMusic(ctx context.Context, reqs []domain.MediaRequest) error {
	client, ak, err := a.requestClient(ctx, domain.RequestMusic)
	if err != nil {
		return nil //nolint:nilerr // the integration is gone: nothing to follow
	}
	queue, err := client.Queue(ctx)
	if err != nil {
		return fmt.Errorf("%s queue: %w", ak, err)
	}
	read := a.store.Read()
	for _, r := range reqs {
		album := r.Kind == domain.RequestAlbum
		next := r
		items, err := read.ItemsWithExternalIDs(ctx, r.Kind.ExternalProvider(), []string{r.Key()})
		if err != nil {
			return err
		}
		item := catalogItem(items[r.Key()], r.Destination)
		t, err := client.GetMusic(ctx, album, r.ArrID)
		var gone *arr.Error
		if errors.As(err, &gone) && gone.Status == http.StatusNotFound {
			continue // removed from Lidarr: the request stays where it was
		}
		if err != nil {
			return fmt.Errorf("%s %s %d: %w", ak, r.Kind, r.ArrID, err)
		}
		next.EpisodesWanted = t.Tracks
		if item != nil {
			next.EpisodesAvailable = t.TrackFiles
			if t.Tracks > 0 {
				next.EpisodesAvailable = min(t.TrackFiles, t.Tracks)
			}
		}
		var size, left float64
		inQueue := false
		for _, d := range queue {
			if album && d.AlbumID == r.ArrID || !album && d.ArrID == r.ArrID {
				size, left, inQueue = size+d.Size, left+d.Left, true
			}
		}
		nextStatus(r, &next, item, inQueue, progress(size, left))
		if err := a.saveProgress(ctx, r, next, item); err != nil {
			return err
		}
	}
	return nil
}

// refreshBooks follows book requests on LazyLibrarian: "Snatched" while it downloads, "Have" once
// filed, then the catalog by ISBN or by title and author.
func (a *App) refreshBooks(ctx context.Context, reqs []domain.MediaRequest) error {
	client, err := a.bookClient(ctx)
	if err != nil {
		return nil //nolint:nilerr // the integration is gone: nothing to follow
	}
	status, err := a.bookStatus(ctx, client)
	if err != nil {
		return fmt.Errorf("%s books: %w", lazyLibrarianName, err)
	}
	books, err := a.store.Read().CatalogBooks(ctx)
	if err != nil {
		return err
	}
	for _, r := range reqs {
		b, known := status[r.ExternalKey]
		next := r
		item := catalogItem(matchBooks(books, r.Title, r.Subtitle, b.ISBN), r.Destination)
		switch {
		case item != nil:
			next.Status, next.ItemID, next.Progress = domain.RequestAvailable, item, 1
		case known && b.InLibrary():
			next.Status, next.Progress = domain.RequestDownloading, 1 // filed: waiting for the scan
		case known && b.Status == "Snatched":
			next.Status = domain.RequestDownloading
		}
		if err := a.saveProgress(ctx, r, next, item); err != nil {
			return err
		}
	}
	return nil
}

// saveProgress writes where a request stands if that changed, and announces it; becoming
// available goes to the activity log.
func (a *App) saveProgress(ctx context.Context, r, next domain.MediaRequest, item *domain.ID) error {
	if sameProgress(r, next) {
		return nil
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
