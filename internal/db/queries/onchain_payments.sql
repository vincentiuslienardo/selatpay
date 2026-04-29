-- name: UpsertOnchainPayment :one
INSERT INTO onchain_payments (
    signature, slot, block_time, from_ata, to_ata, mint, amount,
    reference_pubkey, commitment, intent_id
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT (signature) DO UPDATE SET
    commitment = CASE
        WHEN onchain_payments.commitment < EXCLUDED.commitment THEN EXCLUDED.commitment
        ELSE onchain_payments.commitment
    END,
    intent_id = COALESCE(onchain_payments.intent_id, EXCLUDED.intent_id),
    updated_at = NOW()
RETURNING *;

-- name: GetOnchainPaymentBySignature :one
SELECT * FROM onchain_payments WHERE signature = $1;

-- name: ListUnfinalizedOnchainPayments :many
SELECT * FROM onchain_payments
WHERE commitment <> 'finalized'
ORDER BY created_at
LIMIT $1;

-- name: CountOnchainPaymentsByIntent :one
SELECT COUNT(*) FROM onchain_payments WHERE intent_id = $1;

-- name: GetFinalizedDepositForIntent :one
SELECT * FROM onchain_payments
WHERE intent_id = $1 AND commitment = 'finalized'
ORDER BY created_at
LIMIT 1;

-- name: ListOnchainPaymentsByIntent :many
SELECT * FROM onchain_payments
WHERE intent_id = $1
ORDER BY created_at;
