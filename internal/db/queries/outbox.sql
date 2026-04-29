-- name: PublishOutbox :one
INSERT INTO outbox (topic, aggregate_id, payload, headers)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: ClaimDueOutbox :many
-- Pulls a batch of undelivered messages whose backoff window has elapsed.
-- Caller is expected to hold a pg_try_advisory_lock on the topic so only
-- one dispatcher process drains it; SKIP LOCKED is still here for safety
-- in case two dispatchers race the lock. Ordered by next_attempt_at to
-- preserve fair retry sequencing.
SELECT *
FROM outbox
WHERE topic = $1
  AND delivered_at IS NULL
  AND next_attempt_at <= NOW()
ORDER BY next_attempt_at
FOR UPDATE SKIP LOCKED
LIMIT $2;

-- name: MarkOutboxDelivered :one
UPDATE outbox
SET delivered_at = NOW(),
    last_error   = NULL
WHERE id = $1
RETURNING *;

-- name: ScheduleOutboxRetry :one
UPDATE outbox
SET attempts        = attempts + 1,
    next_attempt_at = $2,
    last_error      = $3
WHERE id = $1
RETURNING *;

-- name: GetOutboxByID :one
SELECT * FROM outbox WHERE id = $1;

-- name: ListUndeliveredOutbox :many
SELECT * FROM outbox
WHERE delivered_at IS NULL
ORDER BY created_at
LIMIT $1;

-- name: ListOutboxByAggregate :many
SELECT * FROM outbox
WHERE aggregate_id = $1
ORDER BY created_at;
