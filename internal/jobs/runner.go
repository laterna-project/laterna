// Package jobs runs the durable job queue: workers, grouped by resource class, claim ready jobs
// from the database, run them, retry them after a transient error and mark them failed once they
// run out of attempts.
package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"runtime/debug"
	"sync"
	"time"

	"github.com/laterna-project/laterna/internal/store"
)

// Handler runs a job on its target (a file ID, an item ID...).
type Handler func(ctx context.Context, target string) error

// ErrPermanent marks an error that retrying will not fix (file gone...).
var ErrPermanent = errors.New("permanent failure")

// Permanent wraps an error so that the job is not retried.
func Permanent(err error) error { return fmt.Errorf("%w: %w", ErrPermanent, err) }

type registration struct {
	class       string
	handler     Handler
	maxAttempts int
	timeout     time.Duration
}

// Option tunes the registration of a job kind.
type Option func(*registration)

// MaxAttempts sets the number of attempts before the job fails for good (default 3).
func MaxAttempts(n int) Option { return func(r *registration) { r.maxAttempts = n } }

// Timeout caps the duration of one run (default 10 minutes).
func Timeout(d time.Duration) Option { return func(r *registration) { r.timeout = d } }

// Runner runs jobs.
type Runner struct {
	store *store.Store
	log   *slog.Logger
	now   func() time.Time
	// idle is the longest time between two checks when nothing wakes a worker. It is only a safety
	// net: enqueuing wakes workers right away.
	idle time.Duration

	mu      sync.Mutex
	kinds   map[string]registration
	classes map[string]int
	wake    map[string]chan struct{}
	wg      sync.WaitGroup
	started bool
	// failed is told about every job that failed for good.
	failed func(ctx context.Context, kind, target string, err error)
	// done is told about the outcome of every run (metrics).
	done func(kind string, elapsed time.Duration, outcome Outcome)
	// around wraps every run (tracing).
	around func(ctx context.Context, kind, target string) (context.Context, func(error))
}

// OnRun registers a hook around every run (tracing). It gets the run's context and returns the one
// to give the job, plus a function called with the outcome.
func (r *Runner) OnRun(fn func(ctx context.Context, kind, target string) (context.Context, func(error))) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.around = fn
}

// Outcome is how a run ended.
type Outcome string

// Outcomes of a run.
const (
	Succeeded Outcome = "succeeded"
	Retried   Outcome = "retried"
	Failed    Outcome = "failed"
)

// OnDone registers who to tell about the outcome of every run (server shutdown excluded).
func (r *Runner) OnDone(fn func(kind string, elapsed time.Duration, outcome Outcome)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.done = fn
}

// OnFailure registers who to tell when a job fails for good (the activity log).
func (r *Runner) OnFailure(fn func(ctx context.Context, kind, target string, err error)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failed = fn
}

// New creates a Runner.
func New(st *store.Store, log *slog.Logger) *Runner {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Runner{
		store: st, log: log, now: time.Now, idle: 5 * time.Second,
		kinds: map[string]registration{}, classes: map[string]int{}, wake: map[string]chan struct{}{},
	}
}

// Class declares a resource class and its number of workers.
func (r *Runner) Class(name string, workers int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.classes[name] = max(1, workers)
	r.wake[name] = make(chan struct{}, 1)
}

// Register ties a job kind to its class and its handler.
func (r *Runner) Register(kind, class string, h Handler, opts ...Option) {
	reg := registration{class: class, handler: h, maxAttempts: 3, timeout: 10 * time.Minute}
	for _, o := range opts {
		o(&reg)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.classes[class]; !ok {
		panic("jobs: unknown class " + class)
	}
	r.kinds[kind] = reg
}

// Enqueue queues a job inside the caller's transaction. Call Kick once the transaction is committed
// to wake the workers.
func (r *Runner) Enqueue(ctx context.Context, q store.Q, kind, target string, priority int) error {
	return r.EnqueueAfter(ctx, q, kind, target, priority, 0)
}

// EnqueueAfter queues a job to run no sooner than delay from now. If the same job is already
// waiting it keeps the nearest deadline, so repeated requests do not push it back.
func (r *Runner) EnqueueAfter(ctx context.Context, q store.Q, kind, target string, priority int, delay time.Duration) error {
	r.mu.Lock()
	reg, ok := r.kinds[kind]
	r.mu.Unlock()
	if !ok {
		return fmt.Errorf("jobs: unknown job kind %q", kind)
	}
	now := r.now()
	return q.EnqueueJob(ctx, store.JobRequest{Kind: kind, Target: target, Class: reg.class, Priority: priority, RunAfter: now.Add(delay)}, now)
}

// Kick wakes all workers (after a committed enqueue).
func (r *Runner) Kick() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, ch := range r.wake {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// Start puts the jobs interrupted by a hard stop back in the queue, then starts the workers. They
// stop when ctx is canceled; Wait waits for them.
func (r *Runner) Start(ctx context.Context) error {
	r.mu.Lock()
	kinds := maps.Clone(r.kinds)
	r.mu.Unlock()
	err := r.store.Write(ctx, func(q store.Q) error {
		// A kind that moved to another class between versions: its jobs follow.
		for kind, reg := range kinds {
			if err := q.SetJobsClass(ctx, kind, reg.class); err != nil {
				return err
			}
		}
		return q.RequeueRunningJobs(ctx, r.now())
	})
	if err != nil {
		return fmt.Errorf("jobs: resuming interrupted jobs: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started {
		return errors.New("jobs: already started")
	}
	r.started = true
	for class, workers := range r.classes {
		for range workers {
			r.wg.Go(func() { r.work(ctx, class) })
		}
	}
	return nil
}

// Wait waits for the workers to stop.
func (r *Runner) Wait() { r.wg.Wait() }

func (r *Runner) work(ctx context.Context, class string) {
	for ctx.Err() == nil {
		job, ok, err := r.claim(ctx, class)
		if err != nil {
			if ctx.Err() == nil {
				r.log.Error("jobs: cannot claim", "class", class, "err", err)
			}
			r.sleep(ctx, class, r.idle)
			continue
		}
		if !ok {
			r.sleep(ctx, class, r.untilNext(ctx, class))
			continue
		}
		r.execute(ctx, job)
	}
}

// claim only takes the write lock when a job is ready.
func (r *Runner) claim(ctx context.Context, class string) (store.Job, bool, error) {
	next, err := r.store.Read().NextJobTime(ctx, class)
	if err != nil || next.IsZero() || next.After(r.now()) {
		return store.Job{}, false, err
	}
	var job store.Job
	var ok bool
	err = r.store.Write(ctx, func(q store.Q) error {
		var err error
		job, ok, err = q.ClaimJob(ctx, class, r.now())
		return err
	})
	return job, ok, err
}

func (r *Runner) untilNext(ctx context.Context, class string) time.Duration {
	next, err := r.store.Read().NextJobTime(ctx, class)
	if err != nil || next.IsZero() {
		return r.idle
	}
	return min(r.idle, max(time.Millisecond, next.Sub(r.now())))
}

func (r *Runner) sleep(ctx context.Context, class string, d time.Duration) {
	r.mu.Lock()
	wake := r.wake[class]
	r.mu.Unlock()
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-wake:
	case <-t.C:
	}
}

func (r *Runner) execute(ctx context.Context, job store.Job) {
	r.mu.Lock()
	reg, known := r.kinds[job.Kind]
	r.mu.Unlock()
	log := r.log.With("job", job.ID, "kind", job.Kind, "target", job.Target, "attempt", job.Attempts)
	if !known {
		_ = r.finish(ctx, job, Permanent(errors.New("unknown job kind")), 1, log)
		return
	}
	start := time.Now()
	runCtx, cancel := context.WithTimeout(ctx, reg.timeout)
	r.mu.Lock()
	around := r.around
	r.mu.Unlock()
	end := func(error) {}
	if around != nil {
		runCtx, end = around(runCtx, job.Kind, job.Target)
	}
	err := safeRun(runCtx, reg.handler, job.Target)
	end(err)
	cancel()
	if ctx.Err() != nil {
		// Server shutdown: the job is picked up again at the next start, without penalty.
		r.requeue(ctx, job, log)
		return
	}
	elapsed := time.Since(start)
	if err == nil {
		log.Debug("job done", "duration", elapsed.Round(time.Millisecond))
	}
	outcome := r.finish(ctx, job, err, reg.maxAttempts, log)
	r.mu.Lock()
	done := r.done
	r.mu.Unlock()
	if done != nil {
		done(job.Kind, elapsed, outcome)
	}
}

// safeRun runs a job and turns a panic into an error.
func safeRun(ctx context.Context, h Handler, target string) (err error) {
	defer func() {
		if v := recover(); v != nil {
			err = Permanent(fmt.Errorf("panic: %v\n%s", v, debug.Stack()))
		}
	}()
	return h(ctx, target)
}

func (r *Runner) finish(ctx context.Context, job store.Job, err error, maxAttempts int, log *slog.Logger) Outcome {
	// Detached context: the outcome is recorded even if the server is shutting down.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	now := r.now()
	var werr error
	outcome := Retried
	switch {
	case err == nil:
		outcome = Succeeded
		werr = r.store.Write(ctx, func(q store.Q) error { return q.CompleteJob(ctx, job.ID) })
	case errors.Is(err, ErrPermanent) || job.Attempts >= maxAttempts:
		outcome = Failed
		log.Warn("job failed for good", "err", err)
		werr = r.store.Write(ctx, func(q store.Q) error { return q.FailJob(ctx, job.ID, err.Error(), now) })
		r.mu.Lock()
		failed := r.failed
		r.mu.Unlock()
		if failed != nil {
			failed(ctx, job.Kind, job.Target, err)
		}
	default:
		delay := backoff(job.Attempts)
		log.Info("job failed, retry scheduled", "err", err, "retry_in", delay)
		werr = r.store.Write(ctx, func(q store.Q) error { return q.RetryJob(ctx, job.ID, now.Add(delay), err.Error(), now) })
	}
	if werr != nil {
		log.Error("jobs: cannot record outcome", "err", werr)
	}
	return outcome
}

func (r *Runner) requeue(ctx context.Context, job store.Job, log *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := r.store.Write(ctx, func(q store.Q) error {
		return q.RetryJob(ctx, job.ID, r.now(), "interrupted by the server stopping", r.now())
	}); err != nil {
		log.Warn("jobs: cannot requeue (resumed at the next start)", "err", err)
	}
}

// backoff is 30 s, 1 min, 2 min... capped at one hour.
func backoff(attempts int) time.Duration {
	d := 30 * time.Second
	for i := 1; i < attempts && d < time.Hour; i++ {
		d *= 2
	}
	return min(d, time.Hour)
}
