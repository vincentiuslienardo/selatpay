package steps

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	ldb "github.com/vincentiuslienardo/selatpay/internal/db"
	dbq "github.com/vincentiuslienardo/selatpay/internal/db/sqlc"
	"github.com/vincentiuslienardo/selatpay/internal/ledger"
	"github.com/vincentiuslienardo/selatpay/internal/saga"
)

// CreditDeposit posts the USDC-side journal that records the merchant's
// claim against the platform when a payer's funds finalize on-chain.
// The posting is idempotent on external_ref (intent_id-derived), so a
// retried step run reuses the existing journal entry rather than
// double-counting the deposit.
type CreditDeposit struct {
	deps Deps
}

func NewCreditDeposit(deps Deps) *CreditDeposit { return &CreditDeposit{deps: deps} }

func (s *CreditDeposit) Name() string { return StepCreditDeposit }

func (s *CreditDeposit) Execute(ctx context.Context, tx pgx.Tx, run saga.Run) (saga.StepResult, error) {
	q := dbq.New(tx)

	intent, err := q.GetPaymentIntentByID(ctx, ldb.PgUUID(run.IntentID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return saga.StepResult{}, saga.Terminal(fmt.Errorf("credit_deposit: intent %s not found", run.IntentID))
		}
		return saga.StepResult{}, fmt.Errorf("credit_deposit: load intent: %w", err)
	}

	deposit, err := q.GetFinalizedDepositForIntent(ctx, ldb.PgUUID(run.IntentID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// No finalized deposit yet — the watcher's enqueue is
			// supposed to ensure one exists, so this means we are
			// running ahead of the watcher's commit. Treat as
			// transient: a retry after backoff will pick the row up.
			return saga.StepResult{}, fmt.Errorf("credit_deposit: no finalized deposit for intent %s", run.IntentID)
		}
		return saga.StepResult{}, fmt.Errorf("credit_deposit: load deposit: %w", err)
	}

	hotWallet, err := s.deps.Ledger.GetAccountByCodeTx(ctx, tx, ledger.AccountHotWalletUSDC, ledger.CurrencyUSDC)
	if err != nil {
		return saga.StepResult{}, saga.Terminal(fmt.Errorf("credit_deposit: hot wallet account: %w", err))
	}
	userFunds, err := s.deps.Ledger.GetAccountByCodeTx(ctx, tx, ledger.AccountLiabilityUserFunds, ledger.CurrencyUSDC)
	if err != nil {
		return saga.StepResult{}, saga.Terminal(fmt.Errorf("credit_deposit: liability account: %w", err))
	}

	intentID := run.IntentID
	entry := ledger.Entry{
		ExternalRef: fmt.Sprintf("credit_deposit:%s", intentID),
		Kind:        "deposit_credit",
		Description: fmt.Sprintf("USDC deposit finalized for intent %s (sig %s)", intentID, deposit.Signature),
		IntentID:    &intentID,
		Lines: []ledger.Line{
			{
				AccountID: hotWallet.ID,
				Amount:    deposit.Amount,
				Currency:  ledger.CurrencyUSDC,
				Direction: ledger.Debit,
			},
			{
				AccountID: userFunds.ID,
				Amount:    deposit.Amount,
				Currency:  ledger.CurrencyUSDC,
				Direction: ledger.Credit,
			},
		},
	}
	if _, err := s.deps.Ledger.PostTx(ctx, tx, entry); err != nil {
		return saga.StepResult{}, fmt.Errorf("credit_deposit: post journal: %w", err)
	}

	if intent.State == dbq.PaymentIntentStatePending {
		if _, err := q.UpdatePaymentIntentState(ctx, dbq.UpdatePaymentIntentStateParams{
			ID:    ldb.PgUUID(intentID),
			State: dbq.PaymentIntentStateFunded,
		}); err != nil {
			return saga.StepResult{}, fmt.Errorf("credit_deposit: advance intent state: %w", err)
		}
	}

	return saga.StepResult{NextStep: StepTriggerPayout}, nil
}
