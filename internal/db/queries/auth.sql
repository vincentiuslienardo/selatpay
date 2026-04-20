-- name: GetActiveAPIKeyByKeyID :one
SELECT k.id, k.merchant_id, k.key_id, k.secret_hash, k.created_at, k.revoked_at,
       m.name AS merchant_name
FROM api_keys k
JOIN merchants m ON m.id = k.merchant_id
WHERE k.key_id = $1
  AND k.revoked_at IS NULL;

-- name: CreateMerchant :one
INSERT INTO merchants (name) VALUES ($1) RETURNING *;

-- name: CreateAPIKey :one
INSERT INTO api_keys (merchant_id, key_id, secret_hash)
VALUES ($1, $2, $3)
RETURNING *;
