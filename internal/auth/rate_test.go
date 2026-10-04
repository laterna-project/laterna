package auth

import (
	"sync"
	"testing"
	"time"
)

func TestRate(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	r := NewRate(3, 10*time.Second)
	r.SetClock(func() time.Time { return now })
	for i := range 3 {
		if _, ok := r.Allow("ip:a"); !ok {
			t.Fatalf("request %d refused", i+1)
		}
	}
	if wait, ok := r.Allow("ip:a"); ok || wait != 10*time.Second {
		t.Errorf("fourth request: %v %v", wait, ok)
	}
	if _, ok := r.Allow("ip:b"); !ok {
		t.Error("another address was slowed down")
	}
	now = now.Add(5 * time.Second)
	if wait, ok := r.Allow("ip:a"); ok || wait != 5*time.Second {
		t.Errorf("after 5 s: %v %v", wait, ok)
	}
	now = now.Add(5 * time.Second)
	if _, ok := r.Allow("ip:a"); !ok {
		t.Error("refused although a token came back")
	}
	// Bucket full again: the key is forgotten.
	now = now.Add(time.Hour)
	r.Allow("ip:c")
	r.mu.Lock()
	n := len(r.buckets)
	r.mu.Unlock()
	if n != 1 {
		t.Errorf("%d keys kept", n)
	}
}

// At most two argon2id runs at a time.
func TestArgonSlots(t *testing.T) {
	var wg sync.WaitGroup
	for range 6 {
		wg.Go(func() {
			if _, err := HashPassword("a-strong-password"); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if len(argonSlots) != 0 {
		t.Error("slot not released")
	}
}
