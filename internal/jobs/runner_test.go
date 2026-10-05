package jobs

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/laterna-project/laterna/internal/store"
)

func newRunner(t *testing.T) (*Runner, *store.Store) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	r := New(st, nil)
	r.idle = 20 * time.Millisecond
	return r, st
}

func enqueue(t *testing.T, r *Runner, st *store.Store, kind, target string) {
	t.Helper()
	ctx := context.Background()
	if err := st.Write(ctx, func(q store.Q) error { return r.Enqueue(ctx, q, kind, target, 0) }); err != nil {
		t.Fatal(err)
	}
	r.Kick()
}

func start(t *testing.T, r *Runner) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	if err := r.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); r.Wait() })
	return cancel
}

func eventually(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition never met")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func states(t *testing.T, st *store.Store) map[string]int {
	t.Helper()
	counts, err := st.Read().CountJobs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]int{}
	for _, c := range counts {
		m[c.State] += c.N
	}
	return m
}

func TestRunsJobsAndDeletesThem(t *testing.T) {
	r, st := newRunner(t)
	r.Class("io", 2)
	var mu sync.Mutex
	seen := map[string]int{}
	r.Register("analyse", "io", func(_ context.Context, target string) error {
		mu.Lock()
		defer mu.Unlock()
		seen[target]++
		return nil
	})
	start(t, r)
	for _, target := range []string{"a", "b", "c"} {
		enqueue(t, r, st, "analyse", target)
	}
	eventually(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(seen) == 3 })
	eventually(t, func() bool { return len(states(t, st)) == 0 })
	if seen["a"] != 1 {
		t.Errorf("\"a\" ran %d times", seen["a"])
	}
}

func TestRetryThenPermanentFailure(t *testing.T) {
	r, st := newRunner(t)
	r.Class("io", 1)
	now := time.Now()
	var clock atomic.Int64
	clock.Store(now.UnixNano())
	r.now = func() time.Time { return time.Unix(0, clock.Load()) }
	var calls atomic.Int32
	r.Register("fragile", "io", func(context.Context, string) error {
		calls.Add(1)
		return errors.New("disk busy")
	}, MaxAttempts(2))
	r.Register("broken", "io", func(context.Context, string) error {
		return Permanent(errors.New("file gone"))
	})
	var mu sync.Mutex
	outcomes := map[string][]Outcome{}
	r.OnDone(func(kind string, _ time.Duration, o Outcome) {
		mu.Lock()
		outcomes[kind] = append(outcomes[kind], o)
		mu.Unlock()
	})
	start(t, r)
	enqueue(t, r, st, "fragile", "x")
	enqueue(t, r, st, "broken", "y")

	eventually(t, func() bool { return calls.Load() == 1 && states(t, st)["failed"] == 1 })
	// The retry waits for its delay: move the clock forward.
	clock.Add(int64(time.Minute))
	r.Kick()
	eventually(t, func() bool { return calls.Load() == 2 && states(t, st)["failed"] == 2 })
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(outcomes["fragile"], []Outcome{Retried, Failed}) || !slices.Equal(outcomes["broken"], []Outcome{Failed}) {
		t.Errorf("outcomes: %v", outcomes)
	}
}

func TestPanicBecomesFailure(t *testing.T) {
	r, st := newRunner(t)
	r.Class("cpu", 1)
	r.Register("boom", "cpu", func(context.Context, string) error { panic("oops") })
	start(t, r)
	enqueue(t, r, st, "boom", "z")
	eventually(t, func() bool { return states(t, st)["failed"] == 1 })
}

func TestConcurrencyPerClass(t *testing.T) {
	r, st := newRunner(t)
	r.Class("ffmpeg", 1)
	r.Class("io", 3)
	var running, peak atomic.Int32
	block := make(chan struct{})
	slow := func(context.Context, string) error {
		n := running.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		<-block
		running.Add(-1)
		return nil
	}
	r.Register("heavy", "ffmpeg", slow)
	start(t, r)
	for _, target := range []string{"1", "2", "3"} {
		enqueue(t, r, st, "heavy", target)
	}
	time.Sleep(100 * time.Millisecond)
	close(block)
	eventually(t, func() bool { return len(states(t, st)) == 0 })
	if peak.Load() != 1 {
		t.Errorf("%d ffmpeg jobs at once, 1 allowed", peak.Load())
	}
}

func TestInterruptedJobsResumeAtStart(t *testing.T) {
	r, st := newRunner(t)
	r.Class("io", 1)
	started := make(chan struct{})
	r.Register("long", "io", func(ctx context.Context, _ string) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	if err := r.Start(ctx); err != nil {
		t.Fatal(err)
	}
	enqueue(t, r, st, "long", "t")
	<-started
	cancel()
	r.Wait()
	if s := states(t, st); s["pending"] != 1 {
		t.Fatalf("after the stop the job must wait to be resumed: %v", s)
	}

	// Restart: the job runs.
	r2 := New(st, nil)
	r2.idle = 20 * time.Millisecond
	r2.Class("io", 1)
	done := make(chan struct{})
	r2.Register("long", "io", func(context.Context, string) error { close(done); return nil })
	start(t, r2)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("interrupted job never resumed")
	}
}

func TestEnqueueUnknownKind(t *testing.T) {
	r, st := newRunner(t)
	ctx := context.Background()
	if err := st.Write(ctx, func(q store.Q) error { return r.Enqueue(ctx, q, "unknown", "x", 0) }); err == nil {
		t.Error("unknown kind accepted")
	}
}

func TestBackoff(t *testing.T) {
	for attempts, want := range map[int]time.Duration{1: 30 * time.Second, 2: time.Minute, 3: 2 * time.Minute, 20: time.Hour} {
		if got := backoff(attempts); got != want {
			t.Errorf("backoff(%d) = %v, want %v", attempts, got, want)
		}
	}
}

// A kind that moves to another class between versions: its queued jobs (filed under the old class)
// are picked up by the new one.
func TestJobsFollowTheirKindToANewClass(t *testing.T) {
	r, st := newRunner(t)
	old := New(st, nil)
	old.Class("io", 1)
	old.Register("file.keyframes", "io", func(context.Context, string) error { return nil })
	enqueue(t, old, st, "file.keyframes", "a")

	var ran atomic.Bool
	r.Class("index", 1)
	r.Register("file.keyframes", "index", func(context.Context, string) error { ran.Store(true); return nil })
	start(t, r)
	eventually(t, ran.Load)
}

// A delayed job waits for its deadline, and asking for it again does not push it back.
func TestEnqueueAfter(t *testing.T) {
	r, st := newRunner(t)
	r.Class("scan", 1)
	var ran atomic.Int64
	r.Register("scan", "scan", func(context.Context, string) error {
		ran.Store(time.Now().UnixNano())
		return nil
	})
	start(t, r)
	ctx := context.Background()
	begin := time.Now()
	for _, delay := range []time.Duration{300 * time.Millisecond, time.Hour} {
		if err := st.Write(ctx, func(q store.Q) error { return r.EnqueueAfter(ctx, q, "scan", "bib", 0, delay) }); err != nil {
			t.Fatal(err)
		}
		r.Kick()
	}
	eventually(t, func() bool { return ran.Load() != 0 })
	// The deadline is stored in milliseconds (rounded down) and the Windows wall clock is coarse,
	// so a few milliseconds of slack are normal. An ignored deadline would start the job right
	// away.
	if d := time.Unix(0, ran.Load()).Sub(begin); d < 290*time.Millisecond {
		t.Errorf("started after %v, before its deadline", d)
	}
	if n := states(t, st)["pending"]; n != 0 {
		t.Errorf("%d jobs still waiting", n)
	}
}
