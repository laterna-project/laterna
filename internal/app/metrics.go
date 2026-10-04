package app

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"runtime"
	"sync"
	"time"

	"github.com/laterna-project/laterna/internal/auth"
	"github.com/laterna-project/laterna/internal/buildinfo"
	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/jobs"
	"github.com/laterna-project/laterna/internal/media/fmp4"
	"github.com/laterna-project/laterna/internal/metrics"
	"github.com/laterna-project/laterna/internal/store"
)

// Server metrics, scraped by Prometheus on /metrics with a dedicated token. What is counted as it
// happens (activity log, playbacks opened, jobs) is kept here; the rest (playbacks in progress, job
// queue, catalog, database) is measured on each scrape.

const (
	keyMetricsToken = "metrics.token"
	// metricsTokenPrefix tells the metrics token apart from a session token.
	metricsTokenPrefix = "lmt_"
	// collectTimeout caps the database reads made for metrics.
	collectTimeout = 2 * time.Second
)

type appMetrics struct {
	reg         *metrics.Registry
	activity    metrics.Counter
	playbacks   metrics.Counter
	jobs        metrics.Counter
	jobDuration metrics.Histogram

	mu sync.Mutex
	// tokenHash is the hash of the metrics token; "" means /metrics is off.
	tokenHash string
}

// jobBuckets are job durations, from 10 ms to 30 min.
var jobBuckets = []float64{0.01, 0.05, 0.1, 0.5, 1, 5, 10, 30, 60, 300, 1800}

// initMetrics creates the registry and what the application measures.
func (a *App) initMetrics(ctx context.Context) error {
	reg := metrics.NewRegistry()
	reg.RegisterRuntime(a.startedAt)
	m := &a.metrics
	m.reg = reg
	m.activity = reg.Counter("laterna_activity_total", "Activity log entries, by kind (sign-ins, failures, playbacks...).", "kind")
	m.playbacks = reg.Counter("laterna_playbacks_started_total", "Playbacks started, by method.", "method")
	m.jobs = reg.Counter("laterna_jobs_finished_total", "Background job runs, by kind and outcome.", "kind", "outcome")
	m.jobDuration = reg.Histogram("laterna_job_duration_seconds", "Duration of background job runs.", jobBuckets, "kind")
	a.jobs.OnDone(func(kind string, elapsed time.Duration, outcome jobs.Outcome) {
		m.jobs.Inc(kind, string(outcome))
		m.jobDuration.Observe(elapsed.Seconds(), kind)
	})

	reg.GaugeFunc("laterna_build_info", "Server version.", []string{"version", "commit", "go_version"}, func(_ context.Context, emit func(float64, ...string)) {
		emit(1, buildinfo.Version, buildinfo.Commit(), runtime.Version())
	})
	reg.GaugeFunc("laterna_playbacks_active", "Active playbacks, by method.", []string{"method"}, func(_ context.Context, emit func(float64, ...string)) {
		counts := map[string]int{}
		for _, p := range a.Playbacks() {
			counts[p.Method]++
		}
		for method, n := range counts {
			emit(float64(n), method)
		}
	})
	reg.GaugeFunc("laterna_ffmpeg_runs", "Running FFmpeg playback processes (remux or transcode).", nil, func(_ context.Context, emit func(float64, ...string)) {
		emit(float64(fmp4.Running()))
	})
	reg.GaugeFunc("laterna_transcodes_active", "Active video transcodes.", nil, func(_ context.Context, emit func(float64, ...string)) {
		emit(float64(a.activeTranscodes()))
	})
	reg.GaugeFunc("laterna_transcodes_limit", "Simultaneous video transcodes allowed.", nil, func(_ context.Context, emit func(float64, ...string)) {
		emit(float64(a.transcodeLimit()))
	})
	reg.GaugeFunc("laterna_backup_last_timestamp_seconds", "Last database backup (Unix seconds).", nil,
		func(_ context.Context, emit func(float64, ...string)) {
			if backups, err := a.Backups(); err == nil && len(backups) > 0 {
				emit(float64(backups[0].CreatedAt.Unix()))
			}
		})
	reg.GaugeFunc("laterna_event_streams", "Clients subscribed to the event stream.", nil, func(_ context.Context, emit func(float64, ...string)) {
		emit(float64(a.bus.Subscribers()))
	})
	reg.GaugeFunc("laterna_database_bytes", "Size of the database and of its journal.", nil, func(_ context.Context, emit func(float64, ...string)) {
		emit(float64(a.store.Size()))
	})
	reg.GaugeFunc("laterna_jobs", "Queued background jobs, by kind and state.", []string{"kind", "state"}, func(ctx context.Context, emit func(float64, ...string)) {
		ctx, cancel := context.WithTimeout(ctx, collectTimeout)
		defer cancel()
		counts, err := a.store.Read().CountJobs(ctx)
		if err != nil {
			return
		}
		for _, c := range counts {
			emit(float64(c.N), c.Kind, c.State)
		}
	})
	reg.GaugeFunc("laterna_items", "Catalog items present (those with a file: movies, episodes, tracks, books, photos), by kind.", []string{"kind"}, func(ctx context.Context, emit func(float64, ...string)) {
		ctx, cancel := context.WithTimeout(ctx, collectTimeout)
		defer cancel()
		counts, err := a.store.Read().CountPresentItems(ctx)
		if err != nil {
			return
		}
		for kind, n := range counts {
			emit(float64(n), string(kind))
		}
	})

	hash, _, err := a.store.Read().Setting(ctx, keyMetricsToken)
	m.tokenHash = hash
	return err
}

// Metrics returns the metrics registry (the API layer adds its own to it).
func (a *App) Metrics() *metrics.Registry { return a.metrics.reg }

// MetricsEnabled reports whether /metrics is on.
func (a *App) MetricsEnabled() bool {
	a.metrics.mu.Lock()
	defer a.metrics.mu.Unlock()
	return a.metrics.tokenHash != ""
}

// AuthorizeMetrics reports whether token opens /metrics.
func (a *App) AuthorizeMetrics(token string) bool {
	a.metrics.mu.Lock()
	want := a.metrics.tokenHash
	a.metrics.mu.Unlock()
	return want != "" && subtle.ConstantTimeCompare([]byte(auth.HashToken(token)), []byte(want)) == 1
}

// EnableMetrics turns /metrics on with a new token (the old one stops working), returned only once:
// only its hash is kept.
func (a *App) EnableMetrics(ctx context.Context, p domain.Principal) (string, error) {
	token := metricsTokenPrefix + rand.Text() + rand.Text()
	hash := auth.HashToken(token)
	if err := a.store.Write(ctx, func(q store.Q) error { return q.SetSetting(ctx, keyMetricsToken, hash) }); err != nil {
		return "", err
	}
	a.metrics.mu.Lock()
	a.metrics.tokenHash = hash
	a.metrics.mu.Unlock()
	a.record(ctx, domain.Activity{Kind: domain.ActivitySettingsUpdated, AccountID: &p.Account.ID, Text: domain.T("activity.metrics_enabled")})
	return token, nil
}

// DisableMetrics turns /metrics off.
func (a *App) DisableMetrics(ctx context.Context, p domain.Principal) error {
	if err := a.store.Write(ctx, func(q store.Q) error { return q.DeleteSetting(ctx, keyMetricsToken) }); err != nil {
		return err
	}
	a.metrics.mu.Lock()
	a.metrics.tokenHash = ""
	a.metrics.mu.Unlock()
	a.record(ctx, domain.Activity{Kind: domain.ActivitySettingsUpdated, AccountID: &p.Account.ID, Text: domain.T("activity.metrics_disabled")})
	return nil
}
