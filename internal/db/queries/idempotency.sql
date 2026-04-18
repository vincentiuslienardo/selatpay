-- name: GetIdempotencyKey :one
SELECT * FROM idempotency_keys
WHERE merchant_id = $1 AND key = $2;

-- name: InsertIdempotencyKey :one
INSERT INTO idempotency_keys (merchant_id, key, request_hash, status_code, response_body)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (merchant_id, key) DO NOTHING
RETURNING *;

-- name: DeleteExpiredIdempotencyKeys :execrows
DELETE FROM idempotency_keys WHERE created_at < $1;
