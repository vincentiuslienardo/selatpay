-- +goose Up
-- +goose StatementBegin
-- Bank coordinates needed by the IDR payout rail. NULL-able for now
-- because the MVP merchant table predates this column; the saga's
-- trigger_payout step rejects payouts for merchants whose details
-- are unset, with a Terminal error that surfaces in saga_runs.last_error
-- so ops can populate them and replay rather than the rail bouncing
-- the payout downstream.
ALTER TABLE merchants
    ADD COLUMN bank_code           TEXT,
    ADD COLUMN bank_account_number TEXT,
    ADD COLUMN bank_account_name   TEXT;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE merchants
    DROP COLUMN IF EXISTS bank_account_name,
    DROP COLUMN IF EXISTS bank_account_number,
    DROP COLUMN IF EXISTS bank_code;
-- +goose StatementEnd
