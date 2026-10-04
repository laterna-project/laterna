package app

import (
	"context"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/recommend"
	"github.com/laterna-project/laterna/internal/store"
)

// Recommendations: movies and series close to what the profile watched, and the titles close to a
// given movie or series. Everything is computed here from the catalog: no outside source, and
// nothing leaves the server.

const (
	// recRebuildEvery: the index is rebuilt at most once a minute when the catalog changes (during
	// a scan, the one from a minute ago is good enough).
	recRebuildEvery = time.Minute
	// maxBecauseRows is the number of "Because you watched" rows on the home page.
	maxBecauseRows = 2
	// minBecause is how many close titles it takes to make a row (or the row size, if that is
	// smaller).
	minBecause = 3
	// anchorWatched is the time watched that makes an item the starting point of a row.
	anchorWatched = 20 * time.Minute
	// tasteFor is how long a taste is kept in memory. It is also dropped whenever the profile plays
	// something, marks it played or favorites it; the slow decay of what was watched changes
	// nothing in ten minutes.
	tasteFor                   = 10 * time.Minute
	defaultSimilar, maxSimilar = 20, 50
)

// recIndex is the recommendation index and the catalog generation it reflects.
type recIndex struct {
	mu      sync.Mutex
	index   *recommend.Index
	gen     uint64
	builtAt time.Time
	// catalogGen grows with every catalog change (itemsChanged, libraryChanged).
	catalogGen atomic.Uint64
	// tastes holds the taste of each profile, kept as long as it does not change (reading it again
	// cost three queries on every home page).
	tasteMu sync.Mutex
	tastes  map[domain.ID]taste
}

// recommendIndex returns the index, rebuilt if the catalog changed since (at most once per
// recRebuildEvery).
func (a *App) recommendIndex(ctx context.Context) (*recommend.Index, error) {
	r := &a.recs
	r.mu.Lock()
	defer r.mu.Unlock()
	gen := r.catalogGen.Load()
	if r.index != nil && (r.gen == gen || a.now().Sub(r.builtAt) < recRebuildEvery) {
		return r.index, nil
	}
	feats, err := a.store.Read().Features(ctx)
	if err != nil {
		return nil, err
	}
	start := time.Now()
	r.index, r.gen, r.builtAt = recommend.Build(feats), gen, a.now()
	a.log.DebugContext(ctx, "recommendation index", "items", len(feats), "duration", time.Since(start).Round(time.Millisecond))
	return r.index, nil
}

// taste is what the profile watched (the signals and their weights) and what it has already played
// or started (never recommended). Once in memory it is not modified.
type taste struct {
	signals []domain.TasteSignal
	weights map[domain.ID]float64
	engaged map[domain.ID]bool
	at      time.Time
}

func (a *App) tasteOf(ctx context.Context, profile domain.ID) (taste, error) {
	r := &a.recs
	r.tasteMu.Lock()
	t, ok := r.tastes[profile]
	r.tasteMu.Unlock()
	if ok && a.now().Sub(t.at) < tasteFor {
		return t, nil
	}
	signals, err := a.store.Read().TasteSignals(ctx, profile)
	if err != nil {
		return taste{}, err
	}
	t = taste{signals: signals, weights: recommend.Taste(signals, a.now()), engaged: make(map[domain.ID]bool, len(signals)), at: a.now()}
	for _, s := range signals {
		t.engaged[s.ID] = true
	}
	r.tasteMu.Lock()
	if r.tastes == nil {
		r.tastes = map[domain.ID]taste{}
	}
	r.tastes[profile] = t
	r.tasteMu.Unlock()
	return t, nil
}

// tasteChanged drops the stored taste of a profile: it played something, marked it played or
// favorited it.
func (a *App) tasteChanged(profile domain.ID) {
	a.recs.tasteMu.Lock()
	delete(a.recs.tastes, profile)
	a.recs.tasteMu.Unlock()
}

// visibleOf keeps, in order, the items the profile can see and play, limit at most. Cards are read
// in small batches (what is missing, plus a few): usually everything is visible, and reading the
// cards of every candidate would cost three times as much.
func (a *App) visibleOf(ctx context.Context, v domain.Viewer, scored []recommend.Scored, limit int) ([]domain.ItemView, error) {
	read := a.store.Read()
	var out []domain.ItemView
	for start := 0; start < len(scored) && len(out) < limit; {
		end := min(len(scored), start+limit-len(out)+5)
		ids := make([]domain.ID, 0, end-start)
		for _, s := range scored[start:end] {
			ids = append(ids, s.ID)
		}
		views, err := read.ViewsByID(ctx, v, ids)
		if err != nil {
			return nil, err
		}
		for _, s := range scored[start:end] {
			if view, ok := views[s.ID]; ok && len(out) < limit {
				out = append(out, view)
			}
		}
		start = end
	}
	return out, nil
}

// candidates returns enough candidates for limit to be left once those the profile cannot see are
// removed.
func candidates(limit int) int { return 3*limit + 10 }

// recommendRows returns the recommendation rows of the home page: "Recommended for you", then
// "Because you watched ..." for the last two movies or series watched long enough. A title only
// shows up in one row: the "Because" rows first take the titles closest to their source (which is
// what they promise) and "Recommended for you" takes the rest.
func (a *App) recommendRows(ctx context.Context, v domain.Viewer, rowSize int) ([]HomeRow, error) {
	t, err := a.tasteOf(ctx, v.ProfileID)
	if err != nil || len(t.signals) == 0 {
		return nil, err
	}
	idx, err := a.recommendIndex(ctx)
	if err != nil {
		return nil, err
	}
	shown := map[domain.ID]bool{}
	skip := func(id domain.ID) bool { return t.engaged[id] || shown[id] }
	show := func(items []domain.ItemView) {
		for _, view := range items {
			shown[view.Item.ID] = true
		}
	}
	// Starting points: the most recently watched, for long enough.
	anchors := make([]domain.TasteSignal, 0, len(t.signals))
	for _, s := range t.signals {
		if s.Played || s.Watched >= anchorWatched || s.Episodes >= 2 {
			anchors = append(anchors, s)
		}
	}
	sort.SliceStable(anchors, func(i, j int) bool { return anchors[i].LastAt.After(anchors[j].LastAt) })
	read := a.store.Read()
	var because []HomeRow
	for _, anchor := range anchors {
		if len(because) == maxBecauseRows {
			break
		}
		source, err := read.View(ctx, v, anchor.ID)
		if store.IsNotFound(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		items, err := a.visibleOf(ctx, v, idx.Similar(anchor.ID, skip, candidates(rowSize)), rowSize)
		if err != nil {
			return nil, err
		}
		if len(items) >= min(minBecause, rowSize) {
			show(items)
			id := anchor.ID
			because = append(because, HomeRow{Kind: RowBecauseYouWatched, Title: domain.T("home.because_you_watched", "title", source.Item.Title), Source: &id, Items: items})
		}
	}
	items, err := a.visibleOf(ctx, v, idx.Recommend(t.weights, skip, candidates(rowSize)), rowSize)
	if err != nil {
		return nil, err
	}
	var rows []HomeRow
	if len(items) > 0 {
		rows = append(rows, HomeRow{Kind: RowRecommended, Title: domain.T("home.recommended"), Items: items})
	}
	return append(rows, because...), nil
}

// Similar returns the movies and series closest to a movie or a series (an episode or a season
// stands for its series), closest first; limit at most (0 means 20, 50 at most).
func (a *App) Similar(ctx context.Context, p domain.Principal, id domain.ID, limit int) ([]domain.ItemView, error) {
	v, err := viewerOf(p)
	if err != nil {
		return nil, err
	}
	switch {
	case limit == 0:
		limit = defaultSimilar
	case limit < 0 || limit > maxSimilar:
		return nil, domain.Invalid("request.invalid_limit", "max", maxSimilar)
	}
	view, err := a.store.Read().View(ctx, v, id)
	if store.IsNotFound(err) {
		return nil, domain.NotFound("catalog.item_not_found")
	}
	if err != nil {
		return nil, err
	}
	owner := view.Item.ID
	switch {
	case view.Episode != nil:
		owner = view.Episode.SeriesID
	case view.Season != nil:
		owner = view.Season.SeriesID
	case view.Item.Kind != domain.ItemMovie && view.Item.Kind != domain.ItemSeries:
		return nil, domain.Invalid("catalog.similar_unsupported")
	}
	idx, err := a.recommendIndex(ctx)
	if err != nil {
		return nil, err
	}
	return a.visibleOf(ctx, v, idx.Similar(owner, nil, candidates(limit)), limit)
}

// catalogChanged records that the catalog changed: the recommendation index must be rebuilt.
func (a *App) catalogChanged() { a.recs.catalogGen.Add(1) }
