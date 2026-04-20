-- +goose Up
-- +goose StatementBegin
CREATE TABLE merchants (
    id          UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    name        TEXT         NOT NULL,
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE TABLE api_keys (
    id           UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_id  UUID         NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
    key_id       TEXT         NOT NULL UNIQUE,
    secret_hash  BYTEA        NOT NULL,
    created_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    revoked_at   TIMESTAMPTZ
);
CREATE INDEX api_keys_merchant_idx ON api_keys (merchant_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS api_keys;
DROP TABLE IF EXISTS merchants;
-- +goose StatementEnd
