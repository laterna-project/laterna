package events

import (
	"sync"
	"testing"
)

func TestPublishSubscribe(t *testing.T) {
	b := New[int]()
	a, c := b.Subscribe(4), b.Subscribe(4)
	b.Publish(1)
	b.Publish(2)
	for _, s := range []*Subscription[int]{a, c} {
		if got := []int{<-s.C(), <-s.C()}; got[0] != 1 || got[1] != 2 {
			t.Errorf("got %v", got)
		}
		if s.Lagged() {
			t.Error("no event was dropped")
		}
	}
	a.Close()
	a.Close() // no-op
	if _, open := <-a.C(); open {
		t.Error("channel still open after Close")
	}
	if b.Subscribers() != 1 {
		t.Errorf("%d subscribers, want 1", b.Subscribers())
	}
	b.Publish(3) // a is gone by now
	if <-c.C() != 3 {
		t.Error("next event")
	}
}

// A subscriber that never reads must not block publishers or other subscribers, and it has to
// find out that it missed events.
func TestSlowSubscriberNeverBlocks(t *testing.T) {
	b := New[int]()
	slow, fast := b.Subscribe(1), b.Subscribe(100)
	for i := range 50 {
		b.Publish(i)
	}
	if len(fast.C()) != 50 {
		t.Errorf("fast subscriber got %d events", len(fast.C()))
	}
	if <-slow.C() != 0 || !slow.Lagged() || slow.Lagged() {
		t.Error("the slow subscriber should keep the first event and be warned once")
	}
}

func TestConcurrentUse(t *testing.T) {
	b := New[int]()
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			s := b.Subscribe(8)
			for i := range 100 {
				b.Publish(i)
			}
			s.Close()
		})
	}
	wg.Wait()
	if b.Subscribers() != 0 {
		t.Errorf("%d subscribers left", b.Subscribers())
	}
}
