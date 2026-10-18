-- name: EnqueueJob :exec
INSERT INTO jobs (kind, target, class, priority, state, attempts, run_after, created_at, updated_at)
VALUES (?, ?, ?, ?, 'pending', 0, ?, ?, ?)
ON CONFLICT (kind, target) WHERE state = 'pending'
DO UPDATE SET priority = max(priority, excluded.priority), run_after = min(run_after, excluded.run_after),
              updated_at = excluded.updated_at;

-- name: ClaimJob :one
-- The next ready job of a class, except one whose twin (same kind and target, so same class) is
-- running: the same job never runs twice at once, the waiting one runs after it. Only the first
-- candidates are looked at, so that a long queue costs no more: each running job holds back at most
-- one waiting twin (jobs_pending), so one candidate more than there are running jobs always
-- includes the first one that can run.
UPDATE jobs
SET state = 'running', attempts = attempts + 1, updated_at = sqlc.arg(now)
WHERE id = (SELECT c.id
            FROM (SELECT p.id, p.priority FROM jobs p
                  WHERE p.class = sqlc.arg(class) AND p.state = 'pending' AND p.run_after <= sqlc.arg(now)
                  ORDER BY p.priority DESC, p.id
                  LIMIT (SELECT count(*) FROM jobs x WHERE x.class = sqlc.arg(class) AND x.state = 'running') + 1) c
            JOIN jobs j ON j.id = c.id
            WHERE NOT EXISTS (SELECT 1 FROM jobs r
                              WHERE r.class = sqlc.arg(class) AND r.state = 'running' AND r.kind = j.kind AND r.target = j.target)
            ORDER BY c.priority DESC, c.id
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
-- The deadline of the next waiting job of a class that can run (see ClaimJob).
SELECT CAST(coalesce(min(c.run_after), 0) AS INTEGER) AS next
FROM (SELECT p.id, p.run_after FROM jobs p
      WHERE p.class = sqlc.arg(class) AND p.state = 'pending'
      ORDER BY p.run_after
      LIMIT (SELECT count(*) FROM jobs x WHERE x.class = sqlc.arg(class) AND x.state = 'running') + 1) c
JOIN jobs j ON j.id = c.id
WHERE NOT EXISTS (SELECT 1 FROM jobs r
                  WHERE r.class = sqlc.arg(class) AND r.state = 'running' AND r.kind = j.kind AND r.target = j.target);

-- name: CountJobs :many
SELECT kind, state, count(*) AS n FROM jobs GROUP BY kind, state ORDER BY kind, state;

-- name: SetJobsClass :exec
UPDATE jobs SET class = sqlc.arg(class) WHERE kind = sqlc.arg(kind) AND class <> sqlc.arg(class);
