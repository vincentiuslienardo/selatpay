# ADR-0005: Idempotency with Postgres reservation and Redis fingerprint cache

- Status: Accepted
- Date: 2026-04-21

## Context

A merchant calling `POST /v1/payment_intents` from a flaky network is going to retry. If two retries with the same `Idempotency-Key` race through the API, we must produce one payment intent, not two. The same key submitted with a different request body is a bug on the caller's side and must be flagged loudly, not silently overwritten.

The shape of the problem:

1. First request with a key creates a resource and stores the response.
2. Concurrent retry while the first is still in-flight should block and either return the same response, or fail with a clear "in progress" status.
3. Subsequent retries after completion should return the original response without re-running side effects.
4. A different request body under the same key must return an error.

Spec for production payments APIs (Stripe-style) is well known; the implementation choices are where the bugs are.

## Decision

Two-tier store with Postgres as the source of truth and Redis as a cache for the hot path.

- A Postgres `idempotency_keys` table with `(merchant_id, key)` primary key, `request_fingerprint` (sha256 of canonical body), `state` (`in_progress`, `completed`), `response_status`, `response_body`, `created_at`, `expires_at`.
- The middleware path on `POST` is:
  1. Compute the fingerprint of the canonical request body.
  2. `INSERT ... ON CONFLICT DO NOTHING` to atomically claim the key with `state='in_progress'`. The conflict path reads the existing row.
  3. If we claimed it, run the handler, update the row with `state='completed'` and the response, then return.
  4. If the row already exists and `state='completed'`, compare fingerprints. Match returns the cached response; mismatch returns 422 Unprocessable Entity with `idempotency_key_conflict`.
  5. If the row exists and `state='in_progress'`, return 409 Conflict with `idempotency_in_progress`. Callers retry after the response settles.
- Redis caches `(merchant_id, key) -> {fingerprint, response}` with a TTL matching the Postgres row, so the common "second retry after completion" path avoids a database round trip.
- Redis is invalidated by writing-through on completion. On Redis unavailability the middleware degrades to Postgres-only; correctness is unaffected, latency is.

The idempotency key is per-merchant, not global, so two merchants choosing the same `Idempotency-Key` (e.g. `"order-1"`) do not collide.

## Consequences

- Stripe-equivalent semantics for retries.
- The 409-during-in-flight semantic forces the caller to think about retry timing rather than us inventing a "wait then return" path that holds connections open.
- The fingerprint check catches the worst class of caller bugs (re-using a key for a different request).
- TTL is configurable; default 24 hours, which matches the typical merchant retry budget.

## Alternatives considered

- **Redis-only**. Cheap but loses durability on Redis restart. For a payments API this is wrong by default.
- **Database-only without cache**. Correct but every retry pays a transaction cost. Acceptable performance-wise for an MVP but cheap to add the cache, so we did.
- **Application-level dedup keyed on intent natural key (`merchant_id, external_ref`)**. We do this too at the resource level (the intents table has a `(merchant_id, external_ref)` unique constraint). The idempotency middleware sits one layer above and protects the entire request pipeline including FX quoting and reference allocation, which the natural-key constraint cannot.
