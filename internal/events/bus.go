// Package events is a small in-memory pub/sub used to push events to connected clients.
// Publishing never blocks: a subscriber that falls behind drops events and is told so (Lagged),
// so it can reload everything.
package events

import (
	"sync"
	"sync/atomic"
)

// Bus fans events of type T out to all its subscribers.
type Bus[T any] struct {
	mu   sync.Mutex
	subs map[*Subscription[T]]struct{}
}

// Subscription receives the events published after it was opened.
type Subscription[T any] struct {
	bus    *Bus[T]
	ch     chan T
	lagged atomic.Bool
	once   sync.Once
}

// New returns an empty bus.
func New[T any]() *Bus[T] {
	return &Bus[T]{subs: map[*Subscription[T]]struct{}{}}
}

// Subscribe opens a subscription. Up to buffer events can wait to be read before newer ones
// are dropped. Close ends it.
func (b *Bus[T]) Subscribe(buffer int) *Subscription[T] {
	s := &Subscription[T]{bus: b, ch: make(chan T, max(1, buffer))}
	b.mu.Lock()
	b.subs[s] = struct{}{}
	b.mu.Unlock()
	return s
}

// Publish hands e to every subscriber that has room and flags the others as lagging.
func (b *Bus[T]) Publish(e T) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for s := range b.subs {
		select {
		case s.ch <- e:
		default:
			s.lagged.Store(true)
		}
	}
}

// Subscribers returns the number of open subscriptions.
func (b *Bus[T]) Subscribers() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs)
}

// C returns the event channel. Close closes it.
func (s *Subscription[T]) C() <-chan T { return s.ch }

// Lagged reports, once, that events were dropped since the previous call.
func (s *Subscription[T]) Lagged() bool { return s.lagged.Swap(false) }

// Close ends the subscription and closes its channel.
func (s *Subscription[T]) Close() {
	s.once.Do(func() {
		s.bus.mu.Lock()
		delete(s.bus.subs, s)
		s.bus.mu.Unlock()
		close(s.ch)
	})
}
