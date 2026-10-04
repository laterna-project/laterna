-- Durable job queue. A finished job is deleted; a job that failed for good stays visible (state =
-- 'failed') with its last error.

-- +goose Up
CREATE TABLE jobs (
    id         INTEGER PRIMARY KEY,
    kind       TEXT NOT NULL,
    -- Target of the job (file, item, library...): together with kind, the deduplication key.
    target     TEXT NOT NULL,
    -- Resource class (io, net, cpu...), each with its own concurrency.
    class      TEXT NOT NULL,
    priority   INTEGER NOT NULL DEFAULT 0,
    state      TEXT NOT NULL CHECK (state IN ('pending', 'running', 'failed')),
    attempts   INTEGER NOT NULL DEFAULT 0,
    run_after  INTEGER NOT NULL,
    last_error TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;

-- A single waiting job per (kind, target): asking twice for the same analysis only runs one.
CREATE UNIQUE INDEX jobs_pending ON jobs (kind, target) WHERE state = 'pending';

CREATE INDEX jobs_ready ON jobs (class, state, priority DESC, run_after, id);

-- +goose Down
DROP TABLE jobs;
