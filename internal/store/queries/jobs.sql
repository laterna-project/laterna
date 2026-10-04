-- name: EnqueueJob :exec
INSERT INTO jobs (kind, target, class, priority, state, attempts, run_after, created_at, updated_at)
VALUES (?, ?, ?, ?, 'pending', 0, ?, ?, ?)
ON CONFLICT (kind, target) WHERE state = 'pending'
DO UPDATE SET priority = max(priority, excluded.priority), run_after = min(run_after, excluded.run_after),
              updated_at = excluded.updated_at;

-- name: ClaimJob :one
UPDATE jobs
SET state = 'running', attempts = attempts + 1, updated_at = ?
WHERE id = (SELECT j.id FROM jobs j
            WHERE j.class = ? AND j.state = 'pending' AND j.run_after <= ?
            ORDER BY j.priority DESC, j.id
            LIMIT 1)
RETURNING id, kind, target, attempts;

-- name: DeleteJob :exec
DELETE FROM jobs WHERE id = ?;

-- name: RetryJob :exec
UPDATE jobs SET state = 'pending', run_after = ?, last_error = ?, updated_at = ? WHERE id = ?;

-- name: FailJob :exec
UPDATE jobs SET state = 'failed', last_error = ?, updated_at = ? WHERE id = ?;

-- name: RequeueRunningJobs :exec
UPDATE OR IGNORE jobs SET state = 'pending', updated_at = ? WHERE state = 'running';

-- name: DeleteRunningJobs :exec
DELETE FROM jobs WHERE state = 'running';

-- name: NextJobTime :one
SELECT CAST(coalesce(min(run_after), 0) AS INTEGER) AS next FROM jobs WHERE class = ? AND state = 'pending';

-- name: CountJobs :many
SELECT kind, state, count(*) AS n FROM jobs GROUP BY kind, state ORDER BY kind, state;

-- name: SetJobsClass :exec
UPDATE jobs SET class = sqlc.arg(class) WHERE kind = sqlc.arg(kind) AND class <> sqlc.arg(class);
