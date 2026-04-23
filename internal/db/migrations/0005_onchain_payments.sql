-- +goose Up
-- +goose StatementBegin
CREATE TYPE solana_commitment AS ENUM (
    'processed',
    'confirmed',
    'finalized'
);

CREATE TABLE onchain_payments (
    signature        TEXT              PRIMARY KEY,
    slot             BIGINT            NOT NULL CHECK (slot >= 0),
    block_time       TIMESTAMPTZ,
    from_ata         TEXT              NOT NULL,
    to_ata           TEXT              NOT NULL,
    mint             TEXT              NOT NULL,
    amount           BIGINT            NOT NULL CHECK (amount > 0),
    reference_pubkey TEXT,
    commitment       solana_commitment NOT NULL,
    intent_id        UUID              REFERENCES payment_intents(id) ON DELETE SET NULL,
    created_at       TIMESTAMPTZ       NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ       NOT NULL DEFAULT NOW()
);
CREATE INDEX onchain_payments_reference_idx  ON onchain_payments (reference_pubkey) WHERE reference_pubkey IS NOT NULL;
CREATE INDEX onchain_payments_intent_idx     ON onchain_payments (intent_id)        WHERE intent_id IS NOT NULL;
CREATE INDEX onchain_payments_pending_idx    ON onchain_payments (commitment)       WHERE commitment <> 'finalized';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS onchain_payments;
DROP TYPE IF EXISTS solana_commitment;
-- +goose StatementEnd
