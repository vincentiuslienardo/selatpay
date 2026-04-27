package steps

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	ldb "github.com/vincentiuslienardo/selatpay/internal/db"
	dbq "github.com/vincentiuslienardo/selatpay/internal/db/sqlc"
	"github.com/vincentiuslienardo/selatpay/internal/saga"
)

// ApplyPayoutResult is the Phase 5 stand-in for the IDR-side journal
// entry that books revenue and the merchant payout. Phase 6 will fill
// in the real postings (debit merchant_payable_idr, credit cash_out_idr,
// credit revenue_fx_spread_idr) once the payout rail returns success.
// For now the step only marks the intent paid_out so the rest of the
// saga path — webhook emission and completion — stays exercisable.
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
