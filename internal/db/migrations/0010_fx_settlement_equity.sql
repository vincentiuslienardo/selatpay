-- +goose Up
-- +goose StatementBegin
-- equity_fx_settlement_usdc absorbs the USDC obligation released by
-- apply_payout_result. The Phase 6 model originally credited
-- asset_hot_wallet_usdc on the swap side, which zeroed out the
-- ledger's view of custody on every settled intent and made
-- recon impossible: on-chain the USDC physically stays in the hot
-- wallet (we don't move it out in this MVP — the FX swap is a
-- treasury-side concept). Routing the release into an equity
-- account keeps asset_hot_wallet_usdc tracking actual on-chain
-- balance so recon can diff it meaningfully.
INSERT INTO accounts (code, type, currency) VALUES
    ('equity_fx_settlement_usdc', 'equity', 'USDC')
ON CONFLICT (code, currency) DO NOTHING;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM accounts WHERE code = 'equity_fx_settlement_usdc';
-- +goose StatementEnd
