package auth

import (
	"sync"
	"time"
)

// Rate limits requests per key with a token bucket: burst requests at once, then one more every
// every. Unlike Limiter, which counts failures, it counts every attempt. It slows down anyone
// flooding the public endpoints (logins, device or provider login starts), whichever account they
// aim at. It lives in memory.
type Rate struct {
	burst float64
	every time.Duration
	now   func() time.Time

	mu      sync.Mutex
	buckets map[string]*bucket
	pruned  time.Time
}

type bucket struct {
	tokens float64
	at     time.Time
}

// NewRate allows burst requests at once, then one every every.
func NewRate(burst int, every time.Duration) *Rate {
	return &Rate{burst: float64(burst), every: every, now: time.Now, buckets: map[string]*bucket{}}
}

// SetClock replaces the clock (tests).
func (r *Rate) SetClock(now func() time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.now = now
}

// Allow takes a token from the key's bucket, or returns how long until the next one.
func (r *Rate) Allow(key string) (time.Duration, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	r.prune(now)
	b := r.buckets[key]
	if b == nil {
		b = &bucket{tokens: r.burst, at: now}
		r.buckets[key] = b
	}
	b.tokens = min(r.burst, b.tokens+float64(now.Sub(b.at))/float64(r.every))
	b.at = now
	if b.tokens >= 1 {
		b.tokens--
		return 0, true
	}
	return time.Duration((1 - b.tokens) * float64(r.every)), false
}

// prune forgets, at most once a minute, the keys whose bucket is full again.
func (r *Rate) prune(now time.Time) {
	if now.Sub(r.pruned) < time.Minute {
		return
	}
	r.pruned = now
	full := time.Duration(r.burst * float64(r.every))
	for k, b := range r.buckets {
		if now.Sub(b.at) >= full {
			delete(r.buckets, k)
		}
	}
}
