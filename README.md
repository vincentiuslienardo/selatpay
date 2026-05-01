# Selatpay

Cross-border stablecoin settlement infrastructure on Solana for the Singapore to Indonesia corridor. A Singapore payer funds a USDC payment intent via Solana Pay; the system detects the on-chain deposit at finalized commitment, runs a Postgres-backed saga that posts double-entry ledger entries and triggers an Indonesian rupiah payout, and delivers a signed webhook to the merchant. The Indonesian fiat rail is mocked in this build; everything else (ledger, saga, outbox, webhook signing, Solana monitoring, reconciliation) is real.

This is a runnable MVP, not a production deployment. The deferred concerns (real KMS custody, Travel Rule, sanctions screening) are documented as ADRs rather than half-implemented.

## Quickstart

```bash
cp .env.example .env
make up        # docker compose: postgres, redis, jaeger, solana-test-validator, mock-bank
make build
make test
make demo      # end-to-end happy path
```

The demo creates a mock USDC mint on the local test validator, funds a payer, posts a payment intent through the API, sends the on-chain transfer, runs the saga to completion, and verifies reconciliation. See [`docs/demo.md`](docs/demo.md) for the walkthrough and [`docs/architecture.md`](docs/architecture.md) for the component map.

## Subcommands (single binary)

| Subcommand | Role |
| --- | --- |
| `selatpayd api` | REST gateway, HMAC auth, idempotency middleware |
| `selatpayd watcher` | Solana Pay reference subscriber, commitment tracker |
| `selatpayd orchestrator` | Postgres-backed saga state machine |
| `selatpayd dispatcher` | Outbox-driven HMAC-signed webhook delivery |
| `selatpayd dashboard` | Read-only htmx operations console |
| `selatpayd recon` | On-chain vs ledger reconciliation walker |

## Documentation

| Doc | Purpose |
| --- | --- |
| [`docs/architecture.md`](docs/architecture.md) | Component map, happy-path sequence diagram, data model, failure semantics |
| [`docs/adr/`](docs/adr/README.md) | Architecture Decision Records (chain choice, ledger model, saga, outbox, idempotency, Solana Pay reference flow, custody, compliance, no Anchor program) |
| [`docs/demo.md`](docs/demo.md) | End-to-end demo walkthrough, prereqs, troubleshooting |
| [`api/openapi.yaml`](api/openapi.yaml) | OpenAPI 3.1 spec, HMAC auth scheme, idempotency contract |
| [`CONTRIBUTING.md`](CONTRIBUTING.md) | Commit conventions, test layout, migration workflow |

## Stack

Go 1.22, chi, pgx/v5 with sqlc, Postgres 16, Redis 7, goose for migrations, gagliardetto/solana-go for Solana primitives, OpenTelemetry to OTLP/Jaeger, golangci-lint v2 in CI. Tests use stdlib plus testcontainers-go against real Postgres and a real solana-test-validator.

## License

[MIT](LICENSE).
