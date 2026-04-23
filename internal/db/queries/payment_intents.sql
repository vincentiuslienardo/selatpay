-- name: CreatePaymentIntent :one
INSERT INTO payment_intents (
    merchant_id, external_ref, amount_idr, quoted_amount_usdc, quote_id, state,
    reference_pubkey, reference_secret_enc, recipient_ata
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: GetPaymentIntentByID :one
SELECT * FROM payment_intents WHERE id = $1;

-- name: GetPaymentIntentByMerchantRef :one
SELECT * FROM payment_intents
WHERE merchant_id = $1 AND external_ref = $2;

-- name: UpdatePaymentIntentReference :one
UPDATE payment_intents
SET reference_pubkey = $2,
    reference_secret_enc = $3,
    recipient_ata = $4,
    updated_at = NOW()
WHERE id = $1
RETURNING *;

-- name: UpdatePaymentIntentState :one
UPDATE payment_intents
SET state = $2,
    updated_at = NOW()
WHERE id = $1
RETURNING *;

-- name: ListActiveReferenceIntents :many
SELECT id, reference_pubkey
FROM payment_intents
WHERE state IN ('pending', 'funded')
  AND reference_pubkey IS NOT NULL
ORDER BY created_at;
