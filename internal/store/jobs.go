package store

import (
	"context"
	"errors"
	"time"

	"github.com/laterna-project/laterna/internal/store/sqlc"
)

// Job is a job claimed by a worker.
type Job struct {
	ID       int64
	Kind     string
	Target   string
	Attempts int
}

// JobRequest describes a job to enqueue.
type JobRequest struct {
	Kind     string
	Target   string
	Class    string
	Priority int
	RunAfter time.Time
}

// EnqueueJob queues a job. If the same job (kind, target) is already waiting, the two are merged:
// highest priority, nearest deadline.
func (q Q) EnqueueJob(ctx context.Context, r JobRequest, now time.Time) error {
	runAfter := r.RunAfter
	if runAfter.IsZero() {
		runAfter = now
	}
	return q.q.EnqueueJob(ctx, sqlc.EnqueueJobParams{
		Kind: r.Kind, Target: r.Target, Class: r.Class, Priority: int64(r.Priority),
		RunAfter: toMillis(runAfter), CreatedAt: toMillis(now), UpdatedAt: toMillis(now),
	})
}

// ClaimJob claims the next ready job of a class; ok is false if there is none.
func (q Q) ClaimJob(ctx context.Context, class string, now time.Time) (Job, bool, error) {
	r, err := q.q.ClaimJob(ctx, sqlc.ClaimJobParams{UpdatedAt: toMillis(now), Class: class, RunAfter: toMillis(now)})
	if IsNotFound(err) {
		return Job{}, false, nil
	}
	if err != nil {
		return Job{}, false, err
	}
	return Job{ID: r.ID, Kind: r.Kind, Target: r.Target, Attempts: int(r.Attempts)}, true, nil
}

// CompleteJob removes a finished job.
func (q Q) CompleteJob(ctx context.Context, id int64) error { return q.q.DeleteJob(ctx, id) }

// RetryJob puts a job back in the queue for later. If the same job was requested in the meantime
// (already waiting), this one is simply removed: the other will do the work.
func (q Q) RetryJob(ctx context.Context, id int64, at time.Time, lastError string, now time.Time) error {
	err := translate(q.q.RetryJob(ctx, sqlc.RetryJobParams{RunAfter: toMillis(at), LastError: lastError, UpdatedAt: toMillis(now), ID: id}))
	if errors.Is(err, ErrDuplicate) {
		return q.q.DeleteJob(ctx, id)
	}
	return err
}

// FailJob marks a job as failed for good.
func (q Q) FailJob(ctx context.Context, id int64, lastError string, now time.Time) error {
	return q.q.FailJob(ctx, sqlc.FailJobParams{LastError: lastError, UpdatedAt: toMillis(now), ID: id})
}

// SetJobsClass files the jobs of a kind under a class, so that a kind that moves to another class
// between versions leaves no stray jobs in the old one.
func (q Q) SetJobsClass(ctx context.Context, kind, class string) error {
	return q.q.SetJobsClass(ctx, sqlc.SetJobsClassParams{Class: class, Kind: kind})
}

// RequeueRunningJobs puts interrupted jobs back in the queue (hard stop while they were running).
// Those that already have a twin waiting are removed.
func (q Q) RequeueRunningJobs(ctx context.Context, now time.Time) error {
	if err := q.q.RequeueRunningJobs(ctx, toMillis(now)); err != nil {
		return err
	}
	return q.q.DeleteRunningJobs(ctx)
}

// NextJobTime returns the deadline of the next waiting job of a class (zero if there is none).
func (q Q) NextJobTime(ctx context.Context, class string) (time.Time, error) {
	ms, err := q.q.NextJobTime(ctx, class)
	if err != nil || ms == 0 {
		return time.Time{}, err
	}
	return fromMillis(ms), nil
}

// JobCount is a count of jobs.
type JobCount struct {
	Kind  string
	State string
	N     int
}

// CountJobs counts jobs by kind and state.
func (q Q) CountJobs(ctx context.Context) ([]JobCount, error) {
	rows, err := q.q.CountJobs(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]JobCount, len(rows))
	for i, r := range rows {
		out[i] = JobCount{Kind: r.Kind, State: r.State, N: int(r.N)}
	}
	return out, nil
}
