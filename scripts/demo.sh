#!/usr/bin/env bash
#
# scripts/demo.sh
#
# End-to-end happy-path demo runner. It assumes the local stack
# (Postgres, Redis, Jaeger, solana-test-validator, mock-bank) is
# already up via `make up`, then:
#
#   1. Generates ed25519 keypairs for hot wallet, mint authority,
#      mint, and a fresh payer (cached under scripts/keys/).
#   2. Creates a mock USDC SPL mint on test-validator and funds the
#      payer with 100 USDC.
#   3. Patches .env so selatpayd reads the demo mint and hot wallet.
#   4. Seeds a demo merchant + api key + webhook config.
#   5. Boots api, watcher, orchestrator, dispatcher, and dashboard
#      processes plus a tiny Python webhook listener.
#   6. POSTs a payment intent (HMAC signed) and runs the payer
#      simulator against the returned solana: URL.
#   7. Polls the intent until it reaches "completed" then runs
#      reconciliation.
#
# Exits 0 on a clean run, non-zero on any failure. Re-running is safe:
# keys are cached, idempotent operations short-circuit.
#
# Prereqs: bash, curl, jq, openssl, go, docker, solana CLI v1.18+,
# spl-token, psql, python3 (stdlib only).

set -euo pipefail

# ---------------------------------------------------------------------
# Config
# ---------------------------------------------------------------------

ROOT_DIR="${ROOT_DIR:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
ENV_FILE="${ENV_FILE:-$ROOT_DIR/.env}"
API_BASE="${API_BASE:-http://localhost:8080}"
RPC_URL="${RPC_URL:-http://localhost:8899}"
WS_URL="${WS_URL:-ws://localhost:8900}"
DASHBOARD="${DASHBOARD:-http://localhost:8081}"
WEBHOOK_PORT="${WEBHOOK_PORT:-9999}"

KEYS_DIR="$ROOT_DIR/scripts/keys"
PAYER_KEY="$KEYS_DIR/payer.json"
MINT_KEY="$KEYS_DIR/mint.json"
MINT_AUTH_KEY="$KEYS_DIR/mint-authority.json"
HOT_WALLET_KEY="$KEYS_DIR/hot-wallet.json"
SEED_OUT="$KEYS_DIR/seed.json"
WEBHOOK_LOG="$KEYS_DIR/webhook.log"

mkdir -p "$KEYS_DIR"

note() { printf '\033[1;36m%s\033[0m\n' "$*"; }
die()  { printf '\033[1;31m%s\033[0m\n' "$*" >&2; exit 1; }

require() { command -v "$1" >/dev/null 2>&1 || die "missing required tool: $1"; }
for tool in curl jq openssl go docker solana spl-token psql python3; do require "$tool"; done

# ---------------------------------------------------------------------
# 1. Stack up + build
# ---------------------------------------------------------------------
note "[1/8] bringing the stack up and building selatpayd"
( cd "$ROOT_DIR" && make up >/dev/null )
( cd "$ROOT_DIR" && make build >/dev/null )
BIN="$ROOT_DIR/bin/selatpayd"

# ---------------------------------------------------------------------
# 2. Solana keypairs and mint
# ---------------------------------------------------------------------
note "[2/8] preparing Solana keypairs and the demo USDC mint"

solana config set --url "$RPC_URL" >/dev/null

keygen() {
    local out=$1
    go run "$ROOT_DIR/scripts/keygen" --out "$out"
}

HOT_WALLET_INFO=$(keygen "$HOT_WALLET_KEY")
MINT_AUTH_INFO=$(keygen "$MINT_AUTH_KEY")
MINT_INFO=$(keygen "$MINT_KEY")
PAYER_INFO=$(keygen "$PAYER_KEY")

HOT_WALLET_ADDR=$(printf '%s' "$HOT_WALLET_INFO" | jq -r .pubkey)
HOT_WALLET_BASE58=$(printf '%s' "$HOT_WALLET_INFO" | jq -r .secret_base58)
MINT_AUTH_ADDR=$(printf '%s' "$MINT_AUTH_INFO" | jq -r .pubkey)
MINT_ADDR=$(printf '%s' "$MINT_INFO" | jq -r .pubkey)
PAYER_ADDR=$(printf '%s' "$PAYER_INFO" | jq -r .pubkey)

note "    hot wallet      $HOT_WALLET_ADDR"
note "    mint authority  $MINT_AUTH_ADDR"
note "    mint            $MINT_ADDR"
note "    payer           $PAYER_ADDR"

solana airdrop --url "$RPC_URL" 2 "$MINT_AUTH_ADDR" >/dev/null
solana airdrop --url "$RPC_URL" 2 "$PAYER_ADDR" >/dev/null
solana airdrop --url "$RPC_URL" 2 "$HOT_WALLET_ADDR" >/dev/null

if ! solana --url "$RPC_URL" account "$MINT_ADDR" >/dev/null 2>&1; then
    note "    creating SPL mint with 6 decimals"
    spl-token create-token \
        --url "$RPC_URL" \
        --decimals 6 \
        --mint-authority "$MINT_AUTH_KEY" \
        --fee-payer "$MINT_AUTH_KEY" \
        -- "$MINT_KEY" >/dev/null
fi

# Hot wallet ATA (recipient).
spl-token create-account \
    --url "$RPC_URL" \
    --owner "$HOT_WALLET_ADDR" \
    --fee-payer "$MINT_AUTH_KEY" \
    "$MINT_ADDR" >/dev/null 2>&1 || true

# Payer ATA + 100 USDC.
spl-token create-account \
    --url "$RPC_URL" \
    --owner "$PAYER_ADDR" \
    --fee-payer "$MINT_AUTH_KEY" \
    "$MINT_ADDR" >/dev/null 2>&1 || true
spl-token mint \
    --url "$RPC_URL" \
    --mint-authority "$MINT_AUTH_KEY" \
    --fee-payer "$MINT_AUTH_KEY" \
    --recipient-owner "$PAYER_ADDR" \
    "$MINT_ADDR" 100 >/dev/null

# ---------------------------------------------------------------------
# 3. .env overrides
# ---------------------------------------------------------------------
note "[3/8] applying .env overrides for the demo mint and hot wallet"

if [[ ! -f "$ENV_FILE" ]]; then cp "$ROOT_DIR/.env.example" "$ENV_FILE"; fi

set_env() {
    local key=$1 value=$2
    python3 - "$ENV_FILE" "$key" "$value" <<'PY'
import sys
path, key, value = sys.argv[1], sys.argv[2], sys.argv[3]
with open(path) as f:
    lines = f.readlines()
out, seen = [], False
for line in lines:
    if line.startswith(key + "="):
        out.append(f"{key}={value}\n"); seen = True
    else:
        out.append(line)
if not seen:
    out.append(f"{key}={value}\n")
with open(path, "w") as f:
    f.writelines(out)
PY
}

set_env SELATPAY_USDC_MINT "$MINT_ADDR"
set_env SELATPAY_HOT_WALLET_PUBKEY "$HOT_WALLET_ADDR"
set_env SELATPAY_HOT_WALLET_SECRET_BASE58 "$HOT_WALLET_BASE58"

set -a
# shellcheck disable=SC1090
. "$ENV_FILE"
set +a

# ---------------------------------------------------------------------
# 4. Seed merchant + api key + webhook config
# ---------------------------------------------------------------------
note "[4/8] seeding demo merchant and api key"

go run "$ROOT_DIR/scripts/seed" \
    --pg-url "$SELATPAY_DB_URL" \
    --pepper "$SELATPAY_API_KEY_PEPPER" \
    --merchant-name "Demo Merchant" \
    --webhook-url "http://localhost:$WEBHOOK_PORT/webhook" \
    > "$SEED_OUT"

MERCHANT_ID=$(jq -r .merchant_id "$SEED_OUT")
KEY_ID=$(jq -r .key_id "$SEED_OUT")
SIGN_HEX=$(jq -r .sign_secret_hex "$SEED_OUT")
note "    merchant_id  $MERCHANT_ID"
note "    key_id       $KEY_ID"

# ---------------------------------------------------------------------
# 5. Start selatpayd and a webhook listener
# ---------------------------------------------------------------------
note "[5/8] starting selatpayd subprocesses"

PIDS=()
trap 'kill "${PIDS[@]}" 2>/dev/null || true' EXIT

start_subprocess() {
    local name=$1
    "$BIN" "$name" > "$KEYS_DIR/$name.log" 2>&1 &
    PIDS+=($!)
    note "    started $name (pid ${PIDS[-1]})"
}
start_subprocess api
start_subprocess watcher
start_subprocess orchestrator
start_subprocess dispatcher
start_subprocess dashboard

for _ in $(seq 1 30); do
    if curl -fsS "$API_BASE/healthz" >/dev/null 2>&1; then break; fi
    sleep 1
done
curl -fsS "$API_BASE/healthz" >/dev/null || die "api never came up; see $KEYS_DIR/api.log"

python3 -m http.server "$WEBHOOK_PORT" >"$WEBHOOK_LOG" 2>&1 &
PIDS+=($!)

# ---------------------------------------------------------------------
# 6. Create payment intent
# ---------------------------------------------------------------------
note "[6/8] creating payment intent"

EXTERNAL_REF="demo-$(date +%s)"
BODY=$(jq -nc --arg er "$EXTERNAL_REF" '{external_ref: $er, amount_idr: 1500000, description: "Demo payout: 1.5M IDR"}')
TS=$(date -u +%s)
BODY_HASH=$(printf '%s' "$BODY" | openssl dgst -sha256 | awk '{print $2}')
CANONICAL=$(printf '%s\n%s\n%s\n%s' "$TS" "POST" "/v1/payment_intents" "$BODY_HASH")
SIG=$(printf '%s' "$CANONICAL" | openssl dgst -sha256 -mac HMAC -macopt "hexkey:$SIGN_HEX" -binary | xxd -p -c 256)
IDEM_KEY="demo-$(uuidgen 2>/dev/null || openssl rand -hex 16)"

INTENT=$(curl -fsS -X POST "$API_BASE/v1/payment_intents" \
    -H "Content-Type: application/json" \
    -H "X-Selatpay-Key-Id: $KEY_ID" \
    -H "X-Selatpay-Timestamp: $TS" \
    -H "X-Selatpay-Signature: $SIG" \
    -H "Idempotency-Key: $IDEM_KEY" \
    --data "$BODY")

INTENT_ID=$(printf '%s' "$INTENT" | jq -r .id)
URL=$(printf '%s' "$INTENT" | jq -r .solana_pay_url)
note "    intent_id    $INTENT_ID"
note "    pay url      $URL"

# ---------------------------------------------------------------------
# 7. Pay
# ---------------------------------------------------------------------
note "[7/8] running payer simulator"

go run "$ROOT_DIR/scripts/pay" \
    --rpc "$RPC_URL" \
    --ws "$WS_URL" \
    --url "$URL" \
    --payer "$PAYER_KEY" \
    --commitment finalized

# ---------------------------------------------------------------------
# 8. Poll until completed, then recon
# ---------------------------------------------------------------------
note "[8/8] polling intent until completed"

sign_get() {
    local path=$1
    local ts=$(date -u +%s)
    local body_sha=$(printf '' | openssl dgst -sha256 | awk '{print $2}')
    local canonical=$(printf '%s\n%s\n%s\n%s' "$ts" "GET" "$path" "$body_sha")
    local sig=$(printf '%s' "$canonical" | openssl dgst -sha256 -mac HMAC -macopt "hexkey:$SIGN_HEX" -binary | xxd -p -c 256)
    printf '%s\t%s' "$ts" "$sig"
}

deadline=$((SECONDS + 90))
state=""
while (( SECONDS < deadline )); do
    IFS=$'\t' read -r ts sig <<< "$(sign_get "/v1/payment_intents/$INTENT_ID")"
    state=$(curl -fsS "$API_BASE/v1/payment_intents/$INTENT_ID" \
        -H "X-Selatpay-Key-Id: $KEY_ID" \
        -H "X-Selatpay-Timestamp: $ts" \
        -H "X-Selatpay-Signature: $sig" | jq -r .state)
    note "    state=$state"
    if [[ "$state" == "completed" ]]; then break; fi
    if [[ "$state" == "failed" || "$state" == "expired" ]]; then
        die "intent reached terminal failure state: $state"
    fi
    sleep 2
done

[[ "$state" == "completed" ]] || die "intent did not reach completed in time (last state $state)"

note "running reconciliation"
"$BIN" recon || die "recon walker reported drift"

note ""
note "demo OK"
note "  intent      $INTENT_ID"
note "  dashboard   $DASHBOARD"
note "  webhook log $WEBHOOK_LOG"
