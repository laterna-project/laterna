package auth

import (
	"sync"
	"time"
)

// Limiter slows down repeated attempts (password, profile PIN). After maxFailures failures within
// the window the key is blocked until the window ends, and each new failure extends it. Typical
// keys are "account:<name>" and "ip:<address>". It lives in memory: a restart resets the counters,
// which is fine for a home server.
type Limiter struct {
	maxFailures int
	window      time.Duration
	now         func() time.Time

	mu      sync.Mutex
	entries map[string]*attempts
	pruned  time.Time
}

type attempts struct {
	failures int
	last     time.Time
}

// NewLimiter allows maxFailures failures per window.
func NewLimiter(maxFailures int, window time.Duration) *Limiter {
	return &Limiter{maxFailures: maxFailures, window: window, now: time.Now, entries: map[string]*attempts{}}
}

// SetClock replaces the clock (tests).
func (l *Limiter) SetClock(now func() time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.now = now
}

// Allow reports whether an attempt is allowed for all keys, and if not how long to wait.
func (l *Limiter) Allow(keys ...string) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.prune(now)
	var wait time.Duration
	for _, k := range keys {
		a := l.entries[k]
		if a == nil || a.failures < l.maxFailures {
			continue
		}
		if until := a.last.Add(l.window); until.After(now) {
			wait = max(wait, until.Sub(now))
		}
	}
	return wait, wait == 0
}

// Fail records a failure for each key.
func (l *Limiter) Fail(keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	for _, k := range keys {
		a := l.entries[k]
		if a == nil || now.Sub(a.last) > l.window {
			a = &attempts{}
			l.entries[k] = a
		}
		a.failures++
		a.last = now
	}
}

// Succeed clears the failures of the keys.
func (l *Limiter) Succeed(keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, k := range keys {
		delete(l.entries, k)
	}
}

// prune drops expired entries, at most once per window.
func (l *Limiter) prune(now time.Time) {
	if now.Sub(l.pruned) < l.window {
		return
	}
	for k, a := range l.entries {
		if now.Sub(a.last) > l.window {
			delete(l.entries, k)
		}
	}
	l.pruned = now
}
