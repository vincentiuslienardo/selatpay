-- +goose Up
-- +goose StatementBegin
CREATE TABLE idempotency_keys (
    merchant_id   UUID         NOT NULL,
    key           TEXT         NOT NULL,
    request_hash  BYTEA        NOT NULL,
    status_code   INTEGER      NOT NULL,
    response_body BYTEA        NOT NULL,
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    CONSTRAINT idempotency_keys_pkey PRIMARY KEY (merchant_id, key)
);
CREATE INDEX idempotency_keys_created_at_idx ON idempotency_keys (created_at);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS idempotency_keys;
-- +goose StatementEnd
