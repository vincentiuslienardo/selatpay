-- name: EnqueueSagaRun :one
-- Idempotent saga insert keyed on (intent_id, saga_kind). The ON CONFLICT
-- branch is a no-op update of intent_id (a value it already equals), which
-- forces RETURNING * to fire on conflict — letting the caller treat enqueue
-- and re-enqueue with the same return shape and never have to follow up
-- with a SELECT.
INSERT INTO saga_runs (intent_id, saga_kind, current_step, state, next_run_at)
VALUES ($1, $2, $3, 'pending', NOW())
ON CONFLICT (intent_id, saga_kind) DO UPDATE SET intent_id = EXCLUDED.intent_id
RETURNING *;

-- name: ClaimDueSagaRun :one
-- Claims one due saga and stamps a time-bound lease on it. SKIP LOCKED lets
-- N orchestrator workers run side by side without contention; expired
-- leases are reclaimable so a worker crashing mid-step doesn't strand the
-- saga. Returns no rows when nothing is due — the runner uses pgx.ErrNoRows
-- as its idle signal.
WITH next AS (
    SELECT id
    FROM saga_runs
    WHERE state IN ('pending', 'running')
      AND next_run_at <= NOW()
      AND (lease_until IS NULL OR lease_until < NOW())
    ORDER BY next_run_at
    FOR UPDATE SKIP LOCKED
    LIMIT 1
)
UPDATE saga_runs s
SET state       = 'running',
    lease_owner = sqlc.arg(lease_owner)::TEXT,
    lease_until = NOW() + make_interval(secs => sqlc.arg(lease_seconds)::DOUBLE PRECISION),
    updated_at  = NOW()
FROM next
WHERE s.id = next.id
RETURNING s.*;

-- name: AdvanceSagaStep :one
-- Step succeeded and the saga has more work to do. Resets attempts and
-- releases the lease so the next worker can pick it up immediately.
UPDATE saga_runs
SET current_step  = $2,
    state         = 'pending',
    step_attempts = 0,
    next_run_at   = NOW(),
    lease_owner   = NULL,
    lease_until   = NULL,
    last_error    = NULL,
    updated_at    = NOW()
WHERE id = $1
RETURNING *;

-- name: CompleteSagaRun :one
UPDATE saga_runs
SET state       = 'completed',
    lease_owner = NULL,
    lease_until = NULL,
    last_error  = NULL,
    updated_at  = NOW()
WHERE id = $1
RETURNING *;

-- name: ScheduleSagaRetry :one
-- Step failed but is retryable. Caller computes next_run_at from the attempt
-- count using exponential backoff with jitter; storing the absolute time
-- means dispatcher restarts cannot reset the schedule.
UPDATE saga_runs
SET step_attempts = step_attempts + 1,
    state         = 'pending',
    next_run_at   = $2,
    lease_owner   = NULL,
    lease_until   = NULL,
    last_error    = $3,
    updated_at    = NOW()
WHERE id = $1
RETURNING *;

-- name: FailSagaRun :one
UPDATE saga_runs
SET state       = 'failed',
    lease_owner = NULL,
    lease_until = NULL,
    last_error  = $2,
    updated_at  = NOW()
WHERE id = $1
RETURNING *;

-- name: GetSagaRunByID :one
SELECT * FROM saga_runs WHERE id = $1;

-- name: GetSagaRunByIntent :one
SELECT * FROM saga_runs WHERE intent_id = $1 AND saga_kind = $2;

-- name: ListSagaRunsByState :many
SELECT * FROM saga_runs WHERE state = $1 ORDER BY created_at DESC LIMIT $2;
