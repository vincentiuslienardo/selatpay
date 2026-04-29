package steps

import (
	"context"
	"errors"
	"fmt"
	"math/big"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	ldb "github.com/vincentiuslienardo/selatpay/internal/db"
	dbq "github.com/vincentiuslienardo/selatpay/internal/db/sqlc"
	"github.com/vincentiuslienardo/selatpay/internal/ledger"
	"github.com/vincentiuslienardo/selatpay/internal/saga"
)

// ApplyPayoutResult posts the IDR-side journal once the rail has
// confirmed disbursement. Two journal entries are written, each
// internally balanced in a single currency:
//
//  1. fx_swap_usdc (USDC): debit liability_user_funds_usdc, credit
//     asset_hot_wallet_usdc. Settles the USDC obligation that was
//     opened by credit_deposit; from Selatpay's books the USDC has
//     left custody to fund the cross-currency swap.
//  2. payout_disbursed (IDR): debit expense_fx_swap_idr (the gross
//     IDR equivalent of the released USDC at the quote's mid rate),
//     credit asset_cash_out_idr (what actually leaves Selatpay's IDR
//     float to the merchant), and credit revenue_fx_spread_idr (the
//     spread we earned). When spread is zero the revenue line is
//     omitted to keep the journal minimal.
//
// Both entries use external_refs derived from intent_id so a saga
// replay reuses the existing entries via Ledger.PostTx's
// idempotency on external_ref — exactly-once effect on at-least-once
// execution.
type ApplyPayoutResult struct {
	deps Deps
}

func NewApplyPayoutResult(deps Deps) *ApplyPayoutResult { return &ApplyPayoutResult{deps: deps} }

func (s *ApplyPayoutResult) Name() string { return StepApplyPayoutResult }

func (s *ApplyPayoutResult) Execute(ctx context.Context, tx pgx.Tx, run saga.Run) (saga.StepResult, error) {
	q := dbq.New(tx)

	intent, err := q.GetPaymentIntentByID(ctx, ldb.PgUUID(run.IntentID))
	if err != nil {
		return saga.StepResult{}, fmt.Errorf("apply_payout_result: load intent: %w", err)
	}
	po, err := q.GetPayoutByIntent(ctx, ldb.PgUUID(run.IntentID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return saga.StepResult{}, saga.Terminal(fmt.Errorf("apply_payout_result: payout missing for intent %s", run.IntentID))
		}
		return saga.StepResult{}, fmt.Errorf("apply_payout_result: load payout: %w", err)
	}
	if po.State != dbq.PayoutStateSucceeded {
		// Defensive: trigger_payout is supposed to leave the payout
		// succeeded before advancing here; anything else is a saga
		// invariant violation, not a transient error.
		return saga.StepResult{}, saga.Terminal(fmt.Errorf("apply_payout_result: payout state %s", po.State))
	}
	quote, err := q.GetQuote(ctx, intent.QuoteID)
	if err != nil {
		return saga.StepResult{}, fmt.Errorf("apply_payout_result: load quote: %w", err)
	}

	midIDR := computeMidIDR(intent.QuotedAmountUsdc, quote.RateNum, quote.RateScale)
	spreadIDR := midIDR - po.AmountIdr
	if spreadIDR < 0 {
		// A negative spread means the merchant got more IDR than the
		// mid rate would imply — Selatpay is on the losing side of
		// the FX swap. That's a quoter or rate-feed bug, not a saga
		// retry case.
		return saga.StepResult{}, saga.Terminal(fmt.Errorf(
			"apply_payout_result: negative spread (mid=%d paid=%d)", midIDR, po.AmountIdr))
	}

	if err := s.postFXSwapUSDC(ctx, tx, run.IntentID, intent.QuotedAmountUsdc, po); err != nil {
		return saga.StepResult{}, err
	}
	if err := s.postPayoutDisbursedIDR(ctx, tx, run.IntentID, midIDR, po.AmountIdr, spreadIDR, po); err != nil {
		return saga.StepResult{}, err
	}

	if intent.State == dbq.PaymentIntentStateSettling {
		if _, err := q.UpdatePaymentIntentState(ctx, dbq.UpdatePaymentIntentStateParams{
			ID:    ldb.PgUUID(run.IntentID),
			State: dbq.PaymentIntentStatePaidOut,
		}); err != nil {
			return saga.StepResult{}, fmt.Errorf("apply_payout_result: advance state: %w", err)
		}
	}
	return saga.StepResult{NextStep: StepEmitCompleted}, nil
}

// postFXSwapUSDC books the USDC-side leg of the cross-currency swap.
// The credit lands on equity_fx_settlement_usdc, not on
// asset_hot_wallet_usdc, because the USDC physically stays in the hot
// wallet during settlement (the FX swap is a treasury-side concept,
// not an on-chain transfer). Crediting the hot wallet here would
// zero the ledger's view of custody after every settlement and break
// recon against the on-chain balance. External_ref keys the entry so
// a replay returns the existing entry instead of double-debiting the
// liability.
func (s *ApplyPayoutResult) postFXSwapUSDC(ctx context.Context, tx pgx.Tx, intentID uuid.UUID, amountUSDC int64, po dbq.Payout) error {
	settlement, err := s.deps.Ledger.GetAccountByCodeTx(ctx, tx, ledger.AccountEquityFXSettlement, ledger.CurrencyUSDC)
	if err != nil {
		return saga.Terminal(fmt.Errorf("apply_payout_result: equity_fx_settlement account: %w", err))
	}
	userFunds, err := s.deps.Ledger.GetAccountByCodeTx(ctx, tx, ledger.AccountLiabilityUserFunds, ledger.CurrencyUSDC)
	if err != nil {
		return saga.Terminal(fmt.Errorf("apply_payout_result: user funds account: %w", err))
	}

	id := intentID
	entry := ledger.Entry{
		ExternalRef: fmt.Sprintf("fx_swap_usdc:%s", intentID),
		Kind:        "fx_swap_usdc",
		Description: fmt.Sprintf("USDC obligation released for intent %s (payout %s)", intentID, ldb.FromPgUUID(po.ID)),
		IntentID:    &id,
		Lines: []ledger.Line{
			{AccountID: userFunds.ID, Amount: amountUSDC, Currency: ledger.CurrencyUSDC, Direction: ledger.Debit},
			{AccountID: settlement.ID, Amount: amountUSDC, Currency: ledger.CurrencyUSDC, Direction: ledger.Credit},
		},
	}
	if _, err := s.deps.Ledger.PostTx(ctx, tx, entry); err != nil {
		return fmt.Errorf("apply_payout_result: post fx_swap_usdc: %w", err)
	}
	return nil
}

// postPayoutDisbursedIDR books the IDR-side journal: total IDR cost
// of the swap recognized as expense, with the merchant payout
// crediting cash-out and the spread crediting revenue. The expense
// account here is conceptually the IDR-denominated counterparty of
// the USDC-side credit; in production this would clear through a
// treasury reconciliation rather than booking as gross expense, but
// the showcase keeps both legs ledger-visible for clarity.
func (s *ApplyPayoutResult) postPayoutDisbursedIDR(ctx context.Context, tx pgx.Tx, intentID uuid.UUID, midIDR, paidIDR, spreadIDR int64, po dbq.Payout) error {
	expense, err := s.deps.Ledger.GetAccountByCodeTx(ctx, tx, "expense_fx_swap_idr", ledger.CurrencyIDR)
	if err != nil {
		return saga.Terminal(fmt.Errorf("apply_payout_result: expense_fx_swap account: %w", err))
	}
	cashOut, err := s.deps.Ledger.GetAccountByCodeTx(ctx, tx, ledger.AccountCashOutIDR, ledger.CurrencyIDR)
	if err != nil {
		return saga.Terminal(fmt.Errorf("apply_payout_result: cash_out account: %w", err))
	}
	revenue, err := s.deps.Ledger.GetAccountByCodeTx(ctx, tx, ledger.AccountRevenueFXSpreadIDR, ledger.CurrencyIDR)
	if err != nil {
		return saga.Terminal(fmt.Errorf("apply_payout_result: revenue account: %w", err))
	}

	id := intentID
	lines := []ledger.Line{
		{AccountID: expense.ID, Amount: midIDR, Currency: ledger.CurrencyIDR, Direction: ledger.Debit},
		{AccountID: cashOut.ID, Amount: paidIDR, Currency: ledger.CurrencyIDR, Direction: ledger.Credit},
	}
	if spreadIDR > 0 {
		lines = append(lines, ledger.Line{
			AccountID: revenue.ID, Amount: spreadIDR, Currency: ledger.CurrencyIDR, Direction: ledger.Credit,
		})
	}

	entry := ledger.Entry{
		ExternalRef: fmt.Sprintf("payout_disbursed:%s", intentID),
		Kind:        "payout_disbursed",
		Description: fmt.Sprintf("IDR payout %s for intent %s (mid=%d paid=%d spread=%d)", ldb.FromPgUUID(po.ID), intentID, midIDR, paidIDR, spreadIDR),
		IntentID:    &id,
		Lines:       lines,
	}
	if _, err := s.deps.Ledger.PostTx(ctx, tx, entry); err != nil {
		return fmt.Errorf("apply_payout_result: post payout_disbursed: %w", err)
	}
	return nil
}

// computeMidIDR returns the IDR equivalent of quoted_amount_usdc at
// the mid rate (no spread applied). USDC carries 6 decimals and IDR
// carries none, so the formula is:
//
//	mid_idr = quoted_amount_usdc * rate_num / 10^(USDC_decimals + rate_scale)
//
// big.Int avoids overflow for any rate the quoter can produce.
func computeMidIDR(quotedAmountUSDC int64, rateNum int64, rateScale int16) int64 {
	const usdcDecimals = 6
	num := new(big.Int).SetInt64(quotedAmountUSDC)
	num.Mul(num, big.NewInt(rateNum))

	denomPow := int64(usdcDecimals) + int64(rateScale)
	denom := new(big.Int).Exp(big.NewInt(10), big.NewInt(denomPow), nil)

	out := new(big.Int).Quo(num, denom)
	if !out.IsInt64() {
		// Mid value larger than int64 means rate or amount has
		// grown beyond the schema; surface as zero so the caller's
		// negative-spread guard fails terminally rather than
		// silently truncating.
		return 0
	}
	return out.Int64()
}

