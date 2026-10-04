package app

import (
	"context"
	"strconv"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/store"
)

// Activity log: what happened on the server, for administrators to read. An entry that cannot be
// written is logged and never prevents the action it tells about.

const (
	// activityRetention is how long the activity log is kept.
	activityRetention  = 90 * 24 * time.Hour
	defaultActivityLen = 50
	maxActivityLen     = 200
)

// record appends an entry to the activity log (timestamped by the server).
func (a *App) record(ctx context.Context, e domain.Activity) {
	e.At = a.now()
	a.metrics.activity.Inc(string(e.Kind))
	// Detached context: the entry is written even if the request that caused it is over.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := a.store.Write(ctx, func(q store.Q) error { return q.AddActivity(ctx, e) }); err != nil {
		a.log.WarnContext(ctx, "activity log: cannot write entry", "kind", e.Kind, "err", err)
	}
}

// ActivityQuery describes a page of the activity log.
type ActivityQuery struct {
	// PageToken is the token returned with the previous page; empty starts from the newest.
	PageToken    string
	WarningsOnly bool
	AccountID    *domain.ID
	PageSize     int
}

// ActivityPage is a page of the activity log, newest first.
type ActivityPage struct {
	Entries []domain.Activity
	// NextPageToken is empty on the last page.
	NextPageToken string
}

// Activity reads a page of the activity log.
func (a *App) Activity(ctx context.Context, aq ActivityQuery) (ActivityPage, error) {
	size := aq.PageSize
	switch {
	case size == 0:
		size = defaultActivityLen
	case size < 0 || size > maxActivityLen:
		return ActivityPage{}, domain.Invalid("request.invalid_page_size", "max", maxActivityLen)
	}
	var before int64
	if aq.PageToken != "" {
		n, err := strconv.ParseInt(aq.PageToken, 36, 64)
		if err != nil || n <= 0 {
			return ActivityPage{}, domain.Invalid("request.invalid_page_token")
		}
		before = n
	}
	entries, err := a.store.Read().Activity(ctx, store.ActivityQuery{
		Before: before, WarningsOnly: aq.WarningsOnly, AccountID: aq.AccountID, Limit: size + 1,
	})
	if err != nil {
		return ActivityPage{}, err
	}
	page := ActivityPage{Entries: entries}
	if len(entries) > size {
		page.Entries = entries[:size]
		page.NextPageToken = strconv.FormatInt(page.Entries[size-1].ID, 36)
	}
	return page, nil
}

// idPtr returns a pointer to a copy of the ID.
func idPtr(id domain.ID) *domain.ID { return &id }
