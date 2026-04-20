-- name: CreateQuote :one
INSERT INTO quotes (pair, rate_num, rate_scale, spread_bps, expires_at, signature)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetQuote :one
SELECT * FROM quotes WHERE id = $1;
