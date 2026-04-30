# Architecture Decision Records

Each record captures the context, the decision, the consequences, and the alternatives that were considered. ADRs are immutable once accepted; if a decision changes, a new ADR supersedes the old one and references it.

| ID | Decision | Status |
| --- | --- | --- |
| [0001](0001-solana-as-settlement-chain.md) | Solana as the settlement chain | Accepted |
| [0002](0002-double-entry-ledger-deferred-trigger.md) | Double-entry ledger with a deferred-trigger balance invariant | Accepted |
| [0003](0003-postgres-saga-over-workflow-engine.md) | Postgres-backed saga, not a workflow engine | Accepted |
| [0004](0004-transactional-outbox-advisory-lock-dispatcher.md) | Transactional outbox with an advisory-lock dispatcher | Accepted |
| [0005](0005-idempotency-postgres-redis.md) | Idempotency with Postgres reservation and Redis fingerprint cache | Accepted |
| [0006](0006-solana-pay-reference-key-per-intent.md) | Solana Pay reference key per intent, no HD-wallet sweeper | Accepted |
| [0007](0007-kms-shaped-signer-interface.md) | KMS-shaped signer interface, local ed25519 in MVP | Accepted |
| [0008](0008-travel-rule-and-sanctions-design.md) | Travel Rule and sanctions: production design, MVP stubs | Accepted |
| [0009](0009-no-custom-anchor-program.md) | No custom Anchor program for v1 | Accepted |
