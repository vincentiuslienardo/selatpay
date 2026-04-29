-- name: CreateAccount :one
INSERT INTO accounts (code, type, currency)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetAccountByID :one
SELECT * FROM accounts WHERE id = $1;

-- name: GetAccountByCodeCurrency :one
SELECT * FROM accounts WHERE code = $1 AND currency = $2;

-- name: ListAccounts :many
SELECT * FROM accounts ORDER BY code, currency;

-- name: CreateJournalEntry :one
INSERT INTO journal_entries (external_ref, kind, description, intent_id)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetJournalEntryByExternalRef :one
SELECT * FROM journal_entries WHERE external_ref = $1;

-- name: InsertPosting :one
INSERT INTO postings (journal_entry_id, account_id, amount, currency, direction)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: ListPostingsByEntry :many
SELECT * FROM postings WHERE journal_entry_id = $1 ORDER BY created_at, id;

-- name: ListJournalEntriesByIntent :many
SELECT * FROM journal_entries WHERE intent_id = $1 ORDER BY created_at;

-- name: AccountBalanceByCode :one
-- Variant of AccountBalance keyed on (code, currency) so callers
-- without the account UUID at hand (e.g., recon) don't need a
-- separate lookup. Returns the same balance shape AccountBalance
-- does.
SELECT
    a.id,
    a.code,
    a.type,
    a.currency,
    COALESCE(SUM(
        CASE
            WHEN a.type IN ('asset', 'expense') AND p.direction = 'debit'  THEN  p.amount
            WHEN a.type IN ('asset', 'expense') AND p.direction = 'credit' THEN -p.amount
            WHEN a.type IN ('liability', 'equity', 'revenue') AND p.direction = 'credit' THEN  p.amount
            WHEN a.type IN ('liability', 'equity', 'revenue') AND p.direction = 'debit'  THEN -p.amount
        END
    ), 0)::BIGINT AS balance
FROM accounts a
LEFT JOIN postings p ON p.account_id = a.id
WHERE a.code = $1 AND a.currency = $2
GROUP BY a.id;

-- name: AccountBalance :one
-- Positive balance is expressed in the account's "natural" direction:
-- debit-normal for asset/expense, credit-normal for liability/equity/revenue.
SELECT
    a.id,
    a.code,
    a.type,
    a.currency,
    COALESCE(SUM(
        CASE
            WHEN a.type IN ('asset', 'expense') AND p.direction = 'debit'  THEN  p.amount
            WHEN a.type IN ('asset', 'expense') AND p.direction = 'credit' THEN -p.amount
            WHEN a.type IN ('liability', 'equity', 'revenue') AND p.direction = 'credit' THEN  p.amount
            WHEN a.type IN ('liability', 'equity', 'revenue') AND p.direction = 'debit'  THEN -p.amount
        END
    ), 0)::BIGINT AS balance
FROM accounts a
LEFT JOIN postings p ON p.account_id = a.id
WHERE a.id = $1
GROUP BY a.id;
