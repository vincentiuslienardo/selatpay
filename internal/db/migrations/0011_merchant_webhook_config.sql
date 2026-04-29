-- +goose Up
-- +goose StatementBegin
-- Webhook delivery configuration is per-merchant: each merchant
-- supplies the URL the dispatcher should POST events to and the
-- HMAC secret used to sign request bodies. Both columns are
-- nullable so existing merchants stay valid; the dispatcher Sender
-- skips delivery (and emits a metric) when either is unset, rather
-- than failing every outbox row.
ALTER TABLE merchants
    ADD COLUMN webhook_url    TEXT,
    ADD COLUMN webhook_secret BYTEA;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE merchants
    DROP COLUMN IF EXISTS webhook_secret,
    DROP COLUMN IF EXISTS webhook_url;
-- +goose StatementEnd
