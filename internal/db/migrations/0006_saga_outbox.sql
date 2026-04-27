-- +goose Up
-- +goose StatementBegin
CREATE TYPE saga_state AS ENUM (
    'pending',
    'running',
    'completed',
    'failed'
);

-- saga_runs is the durable state of every long-running settlement workflow.
-- Each row is one saga instance (one payment intent's path from finalized
-- deposit through merchant payout). The orchestrator polls due rows with
-- FOR UPDATE SKIP LOCKED; the lease_owner / lease_until pair lets a worker
-- declare time-bound ownership without holding the row lock for the full
-- step duration. Idempotency is anchored at (intent_id, saga_kind) so a
-- duplicate enqueue from the watcher is a no-op.
CREATE TABLE saga_runs (
    id              UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    intent_id       UUID         NOT NULL REFERENCES payment_intents(id) ON DELETE RESTRICT,
    saga_kind       TEXT         NOT NULL,
    current_step    TEXT         NOT NULL,
    state           saga_state   NOT NULL DEFAULT 'pending',
    step_attempts   INTEGER      NOT NULL DEFAULT 0 CHECK (step_attempts >= 0),
    next_run_at     TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    lease_owner     TEXT,
    lease_until     TIMESTAMPTZ,
    last_error      TEXT,
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    CONSTRAINT saga_runs_intent_kind_key UNIQUE (intent_id, saga_kind)
);
-- Partial index keyed only on rows the orchestrator might still execute.
-- Completed and failed sagas are terminal; excluding them keeps the scan
-- bounded as history grows.
CREATE INDEX saga_runs_due_idx
    ON saga_runs (next_run_at)
    WHERE state IN ('pending', 'running');

-- outbox is the transactional message log. A producer writes a row inside
-- the same Postgres tx that mutates business state; a single dispatcher
-- per topic (held by pg_try_advisory_lock) drains rows in order and posts
-- to the destination. Backoff lives on the row (next_attempt_at, attempts)
-- so dispatcher restarts don't drop schedule state.
CREATE TABLE outbox (
    id               UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    topic            TEXT         NOT NULL,
    aggregate_id     UUID,
    payload          JSONB        NOT NULL,
    headers          JSONB        NOT NULL DEFAULT '{}'::jsonb,
    created_at       TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    next_attempt_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    delivered_at     TIMESTAMPTZ,
    attempts         INTEGER      NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    last_error       TEXT
);
CREATE INDEX outbox_topic_due_idx
    ON outbox (topic, next_attempt_at)
    WHERE delivered_at IS NULL;
CREATE INDEX outbox_aggregate_idx
    ON outbox (aggregate_id)
    WHERE aggregate_id IS NOT NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS outbox;
DROP TABLE IF EXISTS saga_runs;
DROP TYPE IF EXISTS saga_state;
-- +goose StatementEnd
