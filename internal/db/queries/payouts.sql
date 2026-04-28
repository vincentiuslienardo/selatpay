-- name: UpsertPayout :one
-- Creates the payout row on first call, returns the existing row on
-- replay. The ON CONFLICT branch updates intent_id to itself so
-- RETURNING * fires consistently — the caller never has to follow up
-- with a SELECT to learn the persisted state.
INSERT INTO payouts (intent_id, rail, amount_idr, state)
VALUES ($1, $2, $3, 'pending')
ON CONFLICT (intent_id) DO UPDATE SET intent_id = EXCLUDED.intent_id
RETURNING *;

-- name: GetPayoutByIntent :one
SELECT * FROM payouts WHERE intent_id = $1;

-- name: MarkPayoutSubmitting :one
UPDATE payouts
SET state      = 'submitting',
    attempts   = attempts + 1,
    updated_at = NOW()
WHERE id = $1
RETURNING *;

-- name: MarkPayoutSucceeded :one
UPDATE payouts
SET state          = 'succeeded',
    rail_reference = $2,
    last_error     = NULL,
    updated_at     = NOW()
WHERE id = $1
RETURNING *;

-- name: MarkPayoutFailed :one
UPDATE payouts
SET state      = 'failed',
    last_error = $2,
    updated_at = NOW()
WHERE id = $1
RETURNING *;

-- name: ResetPayoutToPending :one
UPDATE payouts
SET state      = 'pending',
    last_error = $2,
    updated_at = NOW()
WHERE id = $1
RETURNING *;
