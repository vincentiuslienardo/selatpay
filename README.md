# Selatpay

Cross-border stablecoin settlement infrastructure for the SG → ID corridor on Solana.

> Portfolio project. Real testnet (Solana devnet) USDC settlement, double-entry ledger, hand-rolled saga + transactional outbox, signed webhooks, on-chain ↔ ledger reconciliation. Indonesian fiat payout is mocked.

The build is being implemented in phases — see `/Users/vincentiuslienardo/.claude/plans/im-applying-as-a-functional-bee.md` for the canonical plan. Architecture, ADRs, and the demo walkthrough land in `docs/` during Phase 8.

## Quickstart (after Phase 0)

```bash
cp .env.example .env
make up       # postgres, redis, jaeger, solana-test-validator, mock-bank
make build
make test
```

## Subcommands (single binary)

| Subcommand | Role |
| --- | --- |
| `selatpayd api` | REST API gateway, HMAC auth, idempotency middleware |
| `selatpayd watcher` | Solana Pay reference subscriber, commitment tracker |
| `selatpayd orchestrator` | Postgres-backed saga state machine |
| `selatpayd dispatcher` | Outbox-driven signed webhook delivery |
| `selatpayd recon` | On-chain ↔ ledger reconciliation |
