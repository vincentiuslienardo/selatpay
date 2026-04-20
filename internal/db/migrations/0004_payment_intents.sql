-- +goose Up
-- +goose StatementBegin
CREATE TABLE quotes (
    id          UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    pair        TEXT         NOT NULL,
    -- rate is stored as a scaled integer (rate_num / 10^rate_scale) so we never
    -- round-trip through float64 on the settlement path.
    rate_num    BIGINT       NOT NULL CHECK (rate_num > 0),
    rate_scale  SMALLINT     NOT NULL CHECK (rate_scale >= 0 AND rate_scale <= 18),
    spread_bps  INTEGER      NOT NULL CHECK (spread_bps >= 0 AND spread_bps <= 10000),
    expires_at  TIMESTAMPTZ  NOT NULL,
    signature   BYTEA        NOT NULL,
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);
CREATE INDEX quotes_pair_idx ON quotes (pair);

CREATE TYPE payment_intent_state AS ENUM (
    'pending',
    'funded',
    'settling',
    'paid_out',
    'completed',
    'expired',
    'failed'
);

CREATE TABLE payment_intents (
    id                    UUID                  PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_id           UUID                  NOT NULL REFERENCES merchants(id) ON DELETE RESTRICT,
    external_ref          TEXT                  NOT NULL,
    amount_idr            BIGINT                NOT NULL CHECK (amount_idr > 0),
    quoted_amount_usdc    BIGINT                NOT NULL CHECK (quoted_amount_usdc > 0),
    quote_id              UUID                  NOT NULL REFERENCES quotes(id) ON DELETE RESTRICT,
    reference_pubkey      TEXT,
    reference_secret_enc  BYTEA,
    recipient_ata         TEXT,
    state                 payment_intent_state  NOT NULL DEFAULT 'pending',
    created_at            TIMESTAMPTZ           NOT NULL DEFAULT NOW(),
    updated_at            TIMESTAMPTZ           NOT NULL DEFAULT NOW(),
    CONSTRAINT payment_intents_merchant_external_ref_key UNIQUE (merchant_id, external_ref)
);
CREATE INDEX payment_intents_state_idx      ON payment_intents (state);
CREATE INDEX payment_intents_merchant_idx   ON payment_intents (merchant_id, created_at DESC);
CREATE INDEX payment_intents_reference_idx  ON payment_intents (reference_pubkey) WHERE reference_pubkey IS NOT NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS payment_intents;
DROP TYPE IF EXISTS payment_intent_state;
DROP TABLE IF EXISTS quotes;
-- +goose StatementEnd
