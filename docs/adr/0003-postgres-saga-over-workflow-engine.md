# ADR-0003: Postgres-backed saga, not a workflow engine

- Status: Accepted
- Date: 2026-04-23

## Context

Settling a single intent crosses several side-effect boundaries: book the deposit credit, swap FX, post the payout debit, call the bank rail, mark the intent paid out, dispatch the merchant webhook. Each step can fail, retry, or partially succeed. The orchestrator must guarantee that (a) progress survives process crash, (b) duplicate runs do not double-spend, (c) failures are visible and retryable with backoff, and (d) only one worker advances a given saga at a time.

The off-the-shelf options are workflow engines: Temporal, Cadence, Inngest, or a homegrown queue plus a state machine. They solve the same problem and bring real value at multi-team scale.

## Decision

We hand-roll the saga in Postgres:

- A `saga_runs` table holds `(intent_id, current_step, step_attempts, next_run_at, lease_owner, lease_until, last_error)`.
- Workers claim work with `SELECT ... FROM saga_runs WHERE next_run_at <= NOW() AND (lease_until IS NULL OR lease_until < NOW()) FOR UPDATE SKIP LOCKED LIMIT N`, then stamp `lease_owner` and `lease_until` in the same transaction. Lease expiry handles crashed workers without a separate liveness probe.
- Each step is a function `(ctx, intentID) -> (next_step, retry_after, err)` registered in a step registry. The runner advances the saga by writing the new `current_step`, the new `next_run_at` (computed from a backoff function), and any side-effect outputs in one transaction.
- Side effects that must survive the step (webhook payloads, payout receipts) go into the outbox in the same transaction (ADR-0004).

Retry semantics: per-step exponential backoff with jitter (`internal/saga/backoff.go`), capped attempt count, distinguishing transient from permanent errors so a permanent error short-circuits the saga to `failed` rather than burning the retry budget.

## Consequences

- Saga state, ledger postings, and outbox writes share one Postgres transaction. No cross-system coordination is needed.
- We can read a saga's full state with a SQL query. No black-box workflow runtime to debug.
- Operational footprint is small: one binary, one database, one set of metrics. Deploying the orchestrator is `selatpayd orchestrator`.
- Step registration is in Go code, so adding a step requires a code deploy. For our scope this is correct; a workflow engine's hot-loadable workflows are overkill.

## Alternatives considered

- **Temporal**. Excellent for complex workflows with many side effects, scheduled timers, and replay-debugging. The price is another service to operate, another SDK to learn, and another failure surface. For a single saga that has at most six steps, the overhead does not pay off in this MVP. The interface (step functions plus a runner) is shaped so that swapping in Temporal later means rewriting the runner, not the steps.
- **Step Functions, Cadence, Inngest**. Same trade-off; cloud-managed flavors add vendor lock-in.
- **In-memory queue plus goroutines**. Loses durability on crash. Rejected immediately for a payments product.
