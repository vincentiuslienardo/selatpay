-- +goose Up
-- +goose StatementBegin
-- Seed the small set of system accounts the saga steps post against.
-- Per-merchant accounts are not seeded here: the merchant-payable view
-- is single-tenanted to merchant_payable_usdc / merchant_payable_idr
-- for the MVP, with merchant attribution carried on journal_entries
-- via intent_id and on the merchants table itself. Adding per-merchant
-- accounts at scale belongs in an ops-side ledger admin flow, not in
-- a schema migration.
INSERT INTO accounts (code, type, currency) VALUES
    ('asset_hot_wallet_usdc',     'asset',     'USDC'),
    ('liability_user_funds_usdc', 'liability', 'USDC'),
    ('merchant_payable_idr',      'liability', 'IDR'),
    ('asset_cash_out_idr',        'asset',     'IDR'),
    ('revenue_fx_spread_idr',     'revenue',   'IDR'),
    ('expense_network_fee_usdc',  'expense',   'USDC')
ON CONFLICT (code, currency) DO NOTHING;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM accounts WHERE code IN (
    'asset_hot_wallet_usdc',
    'liability_user_funds_usdc',
    'merchant_payable_idr',
    'asset_cash_out_idr',
    'revenue_fx_spread_idr',
    'expense_network_fee_usdc'
);
-- +goose StatementEnd
