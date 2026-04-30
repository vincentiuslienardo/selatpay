# ADR-0002: Double-entry ledger with a deferred-trigger balance invariant

- Status: Accepted
- Date: 2026-04-19

## Context

A payments platform that cannot prove its books are balanced is one ledger bug away from a missing customer payout, a reconciliation gap, or a misstated cash position. Most "ledger" problems in payments come down to one of the following: postings that update a balance column directly (so the journal and the balance can diverge), entries that are written without all of their counter-postings (so debits do not equal credits), or postings denominated in a currency that the account itself is not configured to hold.

The MVP needs the books to be correct from day one because every saga step depends on them: the deposit credit posts to user-funds liability, the payout debit pulls from there, FX swap moves equity, fees move to revenue.

## Decision

A three-table append-only ledger:

- `accounts` (chart of accounts), pinned by `(code, currency)`. Account types are `asset`, `liability`, `equity`, `revenue`, `expense`.
- `journal_entries`, immutable header rows with an `external_ref` for idempotency, plus `intent_id` and `kind` (`deposit_credit`, `fx_swap`, `payout_debit`, `fee`, etc.).
- `postings`, the double-entry lines: `(journal_entry_id, account_id, amount, currency, direction)` where `direction` is `debit` or `credit` and `amount` is always positive.

Two invariants enforced in Postgres, not application code:

1. **Currency match (row-local trigger, BEFORE INSERT)**: a posting's `currency` must equal the `currency` of its account. Cross-currency operations (the FX swap) write two journal entries, not mixed-currency postings inside one entry.
2. **Balanced entry (deferred constraint trigger, INITIALLY DEFERRED)**: at transaction commit, every `journal_entry` must satisfy `SUM(debit) = SUM(credit)` per currency. The check is deferred so that intermediate states (postings inserted one row at a time inside a transaction) do not trip the trigger; only the final committed state must balance.

Account balances are not stored as a column. They are computed from postings as a materialized view that the recon job rebuilds. The journal is the single source of truth.

## Consequences

- Any code path that tries to write an unbalanced entry fails the transaction at commit. There is no "balanced if the application remembered to balance it" loophole.
- The ledger is append-only. Reversals are new journal entries that post the inverse, not deletes or updates. This makes audit trivial.
- Balances are O(postings) to recompute, which is fine for an MVP. A production extension would maintain a per-account balance with an INSERT trigger, but the materialized-view approach lets us defer that complexity until volume justifies it.
- Cross-currency flows pay for two journal entries, but we get a clean separation: the IDR side of an FX swap and the USDC side are visibly distinct entries that can be reconciled independently.

## Alternatives considered

- **Single posting row with signed amount**. Loses the explicit debit/credit semantics, makes the SQL for "all debits to account X" awkward, and trades a tiny storage win for clarity loss. Rejected.
- **Application-level invariant checks**. Easy to forget, easy to bypass under "just this once" pressure, and impossible to enforce against direct SQL. Rejected.
- **Event-sourced ledger (Kafka, append log, projections)**. A reasonable choice at scale. For an MVP that already has Postgres and needs ACID across the journal, the saga state, and the outbox, putting the ledger in the same database is a clear win.
