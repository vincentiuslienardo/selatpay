-- +goose Up
-- +goose StatementBegin
-- expense_fx_swap_idr is the IDR-side counterparty for the cross-currency
-- swap that settles a USDC deposit into an IDR payout. It captures the
-- gross IDR equivalent of the released USDC obligation; the spread we
-- earn is recognized separately on revenue_fx_spread_idr.
INSERT INTO accounts (code, type, currency) VALUES
    ('expense_fx_swap_idr', 'expense', 'IDR')
ON CONFLICT (code, currency) DO NOTHING;

CREATE TYPE payout_state AS ENUM (
    'pending',
    'submitting',
    'succeeded',
    'failed'
);

-- One payout row per intent. The unique constraint on intent_id makes
-- the saga's payout step idempotent: the same intent re-entering
-- trigger_payout reuses the existing row instead of producing a second
-- attempt against the bank rail. rail_reference holds whatever the
-- downstream identifier is (transaction id, reference number) so ops
-- and recon can reconcile against the bank's own books.
CREATE TABLE payouts (
    id              UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    intent_id       UUID         NOT NULL UNIQUE REFERENCES payment_intents(id) ON DELETE RESTRICT,
    rail            TEXT         NOT NULL,
    amount_idr      BIGINT       NOT NULL CHECK (amount_idr > 0),
    state           payout_state NOT NULL DEFAULT 'pending',
    rail_reference  TEXT,
    last_error      TEXT,
    attempts        INTEGER      NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);
CREATE INDEX payouts_state_idx ON payouts (state) WHERE state IN ('pending', 'submitting');
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS payouts;
DROP TYPE IF EXISTS payout_state;
DELETE FROM accounts WHERE code = 'expense_fx_swap_idr';
-- +goose StatementEnd
