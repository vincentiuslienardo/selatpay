package ledger

// Codes for the system accounts seeded by migrations 0007, 0008, and
// 0010. Step implementations resolve these to UUIDs at runtime via
// GetAccountByCodeCurrency rather than embedding IDs, so test fixtures
// and prod use the same code paths.
const (
	AccountHotWalletUSDC        = "asset_hot_wallet_usdc"
	AccountLiabilityUserFunds   = "liability_user_funds_usdc"
	AccountMerchantPayableIDR   = "merchant_payable_idr"
	AccountCashOutIDR           = "asset_cash_out_idr"
	AccountRevenueFXSpreadIDR   = "revenue_fx_spread_idr"
	AccountExpenseNetworkFee    = "expense_network_fee_usdc"
	AccountExpenseFXSwapIDR     = "expense_fx_swap_idr"
	AccountEquityFXSettlement   = "equity_fx_settlement_usdc"
)

// Currency identifiers used throughout posting code. Kept as plain
// constants so callers can compose external_refs without a dependency
// on a string-typed currency package.
const (
	CurrencyUSDC = "USDC"
	CurrencyIDR  = "IDR"
)
