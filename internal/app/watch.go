package app

import (
	"context"
	"path/filepath"
	"sync"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/library"
	"github.com/laterna-project/laterna/internal/store"
)

// Library watching: a change under a root triggers a scan of its library once things have calmed
// down (watchQuiet without a new event, watchMaxWait at most after the first one). A long copy is
// only scanned once it is done, and a folder moved in one go only once. The periodic scan remains
// the safety net (network shares that report nothing, lost events).

const (
	// defaultWatchQuiet is the quiet time before scanning.
	defaultWatchQuiet = 10 * time.Second
	// watchMaxWait: past this the scan starts even if changes keep coming.
	watchMaxWait = 10 * time.Minute
	// watchRetry is the delay before trying unreachable roots again (share offline, disk
	// unplugged).
	watchRetry = 15 * time.Minute
)

// resyncWatches asks for the watched folders to be reviewed (libraries or setting changed).
func (a *App) resyncWatches() {
	select {
	case a.watchResync <- struct{}{}:
	default:
	}
}

// watchLibraries watches library folders as long as the setting allows it, until ctx is canceled.
// Everything happens in this loop: the watcher, and the scans waiting for quiet.
func (a *App) watchLibraries(ctx context.Context) {
	var (
		w         *library.Watcher
		changes   <-chan string
		stop      context.CancelFunc
		runs      sync.WaitGroup
		libraryOf = map[string]domain.ID{}
		// pending holds the first change and the scan deadline of each library touched.
		pending = map[domain.ID][2]time.Time{}
	)
	closeWatcher := func() {
		if w == nil {
			return
		}
		stop()
		_ = w.Close()
		runs.Wait()
		w, changes = nil, nil
		a.log.InfoContext(ctx, "folder watching stopped")
	}
	defer closeWatcher()
	retry := time.NewTicker(watchRetry)
	defer retry.Stop()
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	for {
		enabled := a.Settings().WatchLibraries && !a.noAutoScans
		switch {
		case enabled && w == nil:
			var err error
			if w, err = library.NewWatcher(func(err error) {
				a.log.WarnContext(ctx, "folder watching: error", "err", err)
			}); err != nil {
				a.log.WarnContext(ctx, "folder watching unavailable: the periodic scan takes over", "err", err)
				w = nil
				break
			}
			runCtx, cancel := context.WithCancel(ctx)
			stop = cancel // called by closeWatcher, on shutdown or when the setting requires it
			runs.Go(func() { w.Run(runCtx) })
			changes = w.Changes()
			libraryOf = a.syncWatches(ctx, w)
		case !enabled && w != nil:
			closeWatcher()
			clear(pending)
		case w != nil:
			libraryOf = a.syncWatches(ctx, w)
		}
		// Next deadline of a pending scan.
		timer.Stop()
		if len(pending) > 0 {
			next := time.Time{}
			for _, p := range pending {
				if next.IsZero() || p[1].Before(next) {
					next = p[1]
				}
			}
			timer.Reset(max(0, time.Until(next)))
		}
		select {
		case <-ctx.Done():
			return
		case <-a.watchResync:
		case <-retry.C:
		case root := <-changes:
			id, ok := libraryOf[root]
			if !ok {
				continue
			}
			now := time.Now()
			first := now
			if p, ok := pending[id]; ok {
				first = p[0]
			}
			// Still changing: wait for quiet, without going past watchMaxWait.
			due := now.Add(a.watchQuiet)
			if limit := first.Add(watchMaxWait); due.After(limit) {
				due = limit
			}
			pending[id] = [2]time.Time{first, due}
		case now := <-timer.C:
			for id, p := range pending {
				if !p[1].After(now) {
					delete(pending, id)
					a.scanAfterChange(ctx, id)
				}
			}
		}
	}
}

// syncWatches brings the watched folders in line with the library roots and returns the library of
// each root.
func (a *App) syncWatches(ctx context.Context, w *library.Watcher) map[string]domain.ID {
	libraryOf := map[string]domain.ID{}
	libs, err := a.store.Read().Libraries(ctx)
	if err != nil {
		a.log.WarnContext(ctx, "folder watching: cannot read libraries", "err", err)
		return libraryOf
	}
	var roots []string
	for _, l := range libs {
		for _, p := range l.Paths {
			libraryOf[filepath.Clean(p)] = l.ID
			roots = append(roots, p)
		}
	}
	start := time.Now()
	before := w.Dirs()
	failed, err := w.Sync(roots)
	if err != nil {
		a.log.WarnContext(ctx, "folder watching: roots not watched, the periodic scan takes over",
			"paths", failed, "err", err)
	}
	if dirs := w.Dirs(); dirs != before {
		a.log.InfoContext(ctx, "folder watching", "roots", len(roots)-len(failed), "dirs", dirs,
			"duration", time.Since(start).Round(time.Millisecond))
	}
	return libraryOf
}

// scanAfterChange asks for a scan of a library whose folders changed.
func (a *App) scanAfterChange(ctx context.Context, id domain.ID) {
	err := a.store.Write(ctx, func(q store.Q) error {
		return a.jobs.Enqueue(ctx, q, jobScanLibrary, id.String(), priorityBackground)
	})
	if err != nil {
		if ctx.Err() == nil {
			a.log.WarnContext(ctx, "cannot request scan after a folder change", "library", id, "err", err)
		}
		return
	}
	a.log.DebugContext(ctx, "folder changes: scan requested", "library", id)
	a.jobs.Kick()
}
