# ADR-0004: Transactional outbox with an advisory-lock dispatcher

- Status: Accepted
- Date: 2026-04-25

## Context

When the saga completes a step that has an external side effect (notably a merchant webhook), we have to deliver that side effect at-least-once and never lose it on process crash. The classic anti-pattern is to publish the event right after committing the database transaction. If the process dies between the commit and the publish, the database state is correct but the external system is silent forever.

The textbook fix is the transactional outbox: the saga writes the message to a Postgres table inside the same transaction that records the saga's progress, and a separate process drains the table and publishes. The dispatcher must (a) deliver in order per topic if ordering matters, (b) retry with backoff, (c) not let two dispatcher replicas drain the same topic concurrently, and (d) never lose messages on crash.

## Decision

A single `outbox` table: `(id, topic, payload_json, created_at, delivered_at, attempts, next_attempt_at, last_error)`.

- The saga writes to the outbox in the same transaction as the saga state and ledger postings. ACID guarantees the message exists if and only if the state change committed.
- A separate `selatpayd dispatcher` process drains rows. It claims ready rows with `SELECT ... FROM outbox WHERE delivered_at IS NULL AND next_attempt_at <= NOW() ORDER BY id FOR UPDATE SKIP LOCKED LIMIT N`.
- To keep ordering deterministic per topic we run one dispatcher per topic at a time, enforced by `pg_try_advisory_lock(hashtext(topic)::bigint)`. A second replica trying to start fails to acquire the advisory lock and idles. Lease expires automatically when the connection drops, so a crashed dispatcher is replaced without manual intervention.
- Delivery itself is HMAC-signed (ADR scope: see `internal/webhook/sign.go`); attempts are counted, exponential backoff with jitter governs `next_attempt_at`, and a permanent failure is parked for human inspection rather than retried forever.

## Consequences

- Webhook delivery is durable across crashes, decoupled from the saga's transaction commit, and ordered per topic without an external broker.
- We get exactly-once delivery guarantees from the consumer's perspective via the `Idempotency-Key` header on the webhook payload (consumers dedupe).
- The advisory-lock approach scales horizontally per topic, not per process. If we needed parallelism within a topic, we would partition by intent id and take per-partition advisory locks.
- The dispatcher is one more process to deploy. Acceptable because we already had `selatpayd` as a single binary with subcommands.

## Alternatives considered

- **Direct publish after commit**. The bug we are paid to avoid.
- **Listen/Notify**. Postgres `LISTEN/NOTIFY` is a pleasant signal that work is ready, but the notify is best-effort and lost on disconnect. We use polling with a short interval as the source of truth and could add `LISTEN` purely as a wakeup hint in the future.
- **External broker (Kafka, NATS, Redis Streams)**. Adds another stateful system to operate. Worth the cost when fan-out and throughput justify it. For a webhook dispatcher delivering tens of thousands of events per day, Postgres is more than enough.
- **Per-row advisory lock instead of per-topic**. Allows arbitrary parallelism but loses ordering. Re-evaluated only if a topic's volume actually requires it.
