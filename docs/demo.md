# Demo Walkthrough

This walkthrough runs the full Selatpay happy path on a single laptop: a payer in Singapore funds a USDC payment intent via Solana Pay, the watcher detects the deposit, the orchestrator runs the saga, the (mock) Indonesian bank rail acknowledges the payout, the dispatcher delivers a signed webhook, and the reconciliation walker reports zero drift.

The same flow is what `make demo` automates.

## Prerequisites

| Tool | Why | Notes |
| --- | --- | --- |
| Docker + Compose | Postgres, Redis, Jaeger, solana-test-validator, mockbank | `docker compose v2` |
| Go 1.22+ | Build selatpayd and the demo helpers | |
| solana CLI v1.18+ | `solana airdrop`, `spl-token` for the mock USDC mint | `sh -c "$(curl -sSfL https://release.solana.com/stable/install)"` |
| `jq`, `curl`, `openssl`, `psql`, `python3` | Demo orchestration in shell | All present on most dev boxes |

`make demo` checks each tool and fails with a clear message if any are missing.

## What the demo proves

1. **Idempotent intent creation** under HMAC auth. The same `Idempotency-Key` returns the same intent on retry.
2. **Solana Pay binding**. The intent response carries a `solana:` URL whose `reference` parameter is a fresh ed25519 pubkey. The watcher resolves the deposit by querying `getSignaturesForAddress(reference)`.
3. **Saga step-by-step progression**. `credit_deposit` -> `trigger_payout` -> `apply_payout_result` -> `emit_completed`, each in one Postgres transaction with the ledger postings and the outbox row.
4. **Double-entry ledger correctness**. Every journal entry sums to zero per currency; the deferred trigger refuses to commit anything unbalanced.
5. **At-least-once webhook delivery**. The dispatcher signs the body with HMAC-SHA256, sends to the merchant URL, retries with exponential backoff on failure.
6. **On-chain vs ledger reconciliation**. The recon walker compares the hot wallet ATA balance against the credited liability and reports drift. A clean run is the success criterion.

## Running it

```bash
make up              # bring up the stack
make demo            # full happy-path run
```

Re-running is safe. Cached keypairs live under `scripts/keys/` (gitignored); cached `.env` values are kept across runs.

## What you see

The script prints colored banners for each of its eight phases:

```
[1/8] bringing the stack up and building selatpayd
[2/8] preparing Solana keypairs and the demo USDC mint
[3/8] applying .env overrides for the demo mint and hot wallet
[4/8] seeding demo merchant and api key
[5/8] starting selatpayd subprocesses
[6/8] creating payment intent
[7/8] running payer simulator
[8/8] polling intent until completed
demo OK
  intent      <uuid>
  dashboard   http://localhost:8081
  webhook log scripts/keys/webhook.log
```

The payer simulator prints the on-chain signature and an explorer URL so you can verify the transfer exists outside the simulator's word.

## Where to look after the run

| Where | What |
| --- | --- |
| `http://localhost:8081` | Read-only htmx dashboard: intents, postings, payout receipts |
| `http://localhost:16686` | Jaeger UI for the OTEL traces emitted across all subcommands |
| `scripts/keys/api.log`, `watcher.log`, `orchestrator.log`, `dispatcher.log`, `dashboard.log` | Per-process logs |
| `scripts/keys/webhook.log` | Tiny `python -m http.server` capturing the signed webhook body |
| `scripts/keys/seed.json` | Demo merchant id, key id, signing secret (the credentials the script uses) |

`bin/selatpayd recon` re-runs the reconciliation walker on demand; it prints a JSON report with the on-chain balance, the ledger balance, and the drift if any.

## Manual flow

If you prefer to drive it by hand, the steps map onto these endpoints:

```bash
# Create intent (signed). The seed JSON has key_id and sign_secret_hex.
curl -X POST http://localhost:8080/v1/payment_intents \
    -H 'Content-Type: application/json' \
    -H "X-Selatpay-Key-Id: $KEY_ID" \
    -H "X-Selatpay-Timestamp: $(date -u +%s)" \
    -H "X-Selatpay-Signature: $SIG_HEX" \
    -H "Idempotency-Key: $RUN_ID" \
    --data '{"external_ref":"order-1","amount_idr":1500000}'

# Inspect intent
curl http://localhost:8080/v1/payment_intents/$INTENT_ID -H ... # signed GET

# Pay (parses URL, builds TransferChecked, sends)
go run ./scripts/pay --rpc http://localhost:8899 --url "$SOLANA_PAY_URL" --payer scripts/keys/payer.json

# Reconcile
bin/selatpayd recon
```

The HMAC signing scheme is documented inline in `api/openapi.yaml` under the `HmacAuth` security scheme.

## Troubleshooting

| Symptom | Likely cause |
| --- | --- |
| `pg_isready` never returns | `make up` did not finish; check `docker compose ps` |
| `airdrop` fails | local validator not up or rate limit; rerun `make up` |
| Watcher does not credit the deposit | mint mismatch between `.env` and the mint the simulator paid; clean `scripts/keys/` and rerun |
| Webhook not in `webhook.log` | dispatcher could not reach the listener; the demo sets the URL to `localhost`, not `host.docker.internal`, because selatpayd runs on the host |
| Recon reports drift | the deposit landed but the orchestrator did not run; check `orchestrator.log` for the failed step |
