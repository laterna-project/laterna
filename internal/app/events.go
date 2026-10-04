package app

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/events"
)

const (
	// eventBuffer is how many events can wait for a subscriber before it is flagged as lagging.
	eventBuffer = 256
	// maxEventIDs caps the IDs an event lists; beyond that the event says "reload everything".
	maxEventIDs = 100
	// changesEvery batches item changes: a scan produces thousands of them, and clients get one
	// summary per library at this pace.
	changesEvery = 2 * time.Second
)

// pendingChanges gathers the changed items of a library between two broadcasts.
type pendingChanges struct {
	ids       map[domain.ID]struct{}
	truncated bool
}

// changes batches item changes by library.
type changes struct {
	mu   sync.Mutex
	libs map[domain.ID]*pendingChanges
}

func (c *changes) pending(libraryID domain.ID) *pendingChanges {
	if c.libs == nil {
		c.libs = map[domain.ID]*pendingChanges{}
	}
	p := c.libs[libraryID]
	if p == nil {
		p = &pendingChanges{ids: map[domain.ID]struct{}{}}
		c.libs[libraryID] = p
	}
	return p
}

// itemsChanged records changed items. They are announced at the next broadcast.
func (a *App) itemsChanged(libraryID domain.ID, ids ...domain.ID) {
	a.catalogChanged()
	a.changes.mu.Lock()
	defer a.changes.mu.Unlock()
	p := a.changes.pending(libraryID)
	for _, id := range ids {
		if len(p.ids) >= maxEventIDs {
			p.truncated = true
			break
		}
		p.ids[id] = struct{}{}
	}
}

// libraryChanged records that a whole library should be reloaded (items removed, files gone or
// back).
func (a *App) libraryChanged(libraryID domain.ID) {
	a.catalogChanged()
	a.changes.mu.Lock()
	defer a.changes.mu.Unlock()
	a.changes.pending(libraryID).truncated = true
}

// flushChanges broadcasts the gathered changes, one event per library.
func (a *App) flushChanges() {
	a.changes.mu.Lock()
	libs := a.changes.libs
	a.changes.libs = nil
	a.changes.mu.Unlock()
	for lib, p := range libs {
		e := domain.ItemsChanged{LibraryID: lib, Truncated: p.truncated}
		if !p.truncated {
			for id := range p.ids {
				e.ItemIDs = append(e.ItemIDs, id)
			}
			slices.SortFunc(e.ItemIDs, func(x, y domain.ID) int { return bytes.Compare(x[:], y[:]) })
		}
		a.bus.Publish(e)
	}
}

// publishChanges broadcasts item changes at regular intervals until shutdown.
func (a *App) publishChanges(ctx context.Context) {
	t := time.NewTicker(changesEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			a.flushChanges()
			return
		case <-t.C:
			a.flushChanges()
		}
	}
}

// userDataChanged announces a change in a profile's data right away, so that the profile's other
// devices catch up.
func (a *App) userDataChanged(profileID domain.ID, ids []domain.ID) {
	a.tasteChanged(profileID)
	e := domain.UserDataChanged{ProfileID: profileID}
	if len(ids) > maxEventIDs {
		e.Truncated = true
	} else {
		e.ItemIDs = slices.Clone(ids)
	}
	a.bus.Publish(e)
}

// Subscription receives the events meant for one caller.
type Subscription struct {
	sub *events.Subscription[domain.Event]
	p   domain.Principal
}

// Subscribe opens an event subscription for the caller, until Close. The profile is the one of the
// session at the time of subscribing.
func (a *App) Subscribe(p domain.Principal) *Subscription {
	return &Subscription{sub: a.bus.Subscribe(eventBuffer), p: p}
}

// ErrSubscriptionClosed is returned for a closed subscription.
var ErrSubscriptionClosed = errors.New("event subscription closed")

// Next waits for the next event meant for the caller. After events were lost (reading too slowly)
// it first returns domain.Resync. When ctx is canceled it returns its error.
func (s *Subscription) Next(ctx context.Context) (domain.Event, error) {
	for {
		if s.sub.Lagged() {
			return domain.Resync{}, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case e, ok := <-s.sub.C():
			if !ok {
				return nil, ErrSubscriptionClosed
			}
			if s.wants(e) {
				return e, nil
			}
		}
	}
}

// Close ends the subscription.
func (s *Subscription) Close() { s.sub.Close() }

// wants decides whether an event concerns the caller.
func (s *Subscription) wants(e domain.Event) bool {
	switch e := e.(type) {
	case domain.ItemsChanged:
		return s.p.AllowsLibrary(e.LibraryID)
	case domain.Resync, domain.LibrariesChanged:
		return true
	case domain.LibraryScanned:
		return s.p.CanAdminister()
	case domain.UserDataChanged:
		return s.p.Profile != nil && s.p.Profile.ID == e.ProfileID
	case domain.DownloadsChanged:
		return s.p.SessionID == e.SessionID
	case domain.ThemesChanged:
		return e.ProfileID == nil || (s.p.Profile != nil && s.p.Profile.ID == *e.ProfileID)
	}
	return false
}
