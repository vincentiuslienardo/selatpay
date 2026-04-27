package steps

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	ldb "github.com/vincentiuslienardo/selatpay/internal/db"
	dbq "github.com/vincentiuslienardo/selatpay/internal/db/sqlc"
	"github.com/vincentiuslienardo/selatpay/internal/saga"
)

// TriggerPayout is the Phase 5 stand-in for the IDR rail call. It only
// advances the intent state to settling and hands control to the next
// step. Phase 6 replaces the body with a real call into the payout
// router (mock_idr_bank for the local dev loop, production rails in
// deploy). Keeping the stub in place means the saga walks end-to-end
// today without forcing Phase 6 to land before any integration test
// can run.
type TriggerPayout struct {
	deps Deps
}

func NewTriggerPayout(deps Deps) *TriggerPayout { return &TriggerPayout{deps: deps} }

func (s *TriggerPayout) Name() string { return StepTriggerPayout }

func (s *TriggerPayout) Execute(ctx context.Context, tx pgx.Tx, run saga.Run) (saga.StepResult, error) {
	q := dbq.New(tx)
	intent, err := q.GetPaymentIntentByID(ctx, ldb.PgUUID(run.IntentID))
	if err != nil {
		return saga.StepResult{}, fmt.Errorf("trigger_payout: load intent: %w", err)
	}
	if intent.State == dbq.PaymentIntentStateFunded {
		if _, err := q.UpdatePaymentIntentState(ctx, dbq.UpdatePaymentIntentStateParams{
			ID:    ldb.PgUUID(run.IntentID),
			State: dbq.PaymentIntentStateSettling,
		}); err != nil {
			return saga.StepResult{}, fmt.Errorf("trigger_payout: advance state: %w", err)
		}
	}
	return saga.StepResult{NextStep: StepApplyPayoutResult}, nil
}
