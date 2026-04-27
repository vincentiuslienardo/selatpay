package steps

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	ldb "github.com/vincentiuslienardo/selatpay/internal/db"
	dbq "github.com/vincentiuslienardo/selatpay/internal/db/sqlc"
	"github.com/vincentiuslienardo/selatpay/internal/outbox"
	"github.com/vincentiuslienardo/selatpay/internal/saga"
)

// EmitCompleted is the saga's terminal step. It publishes an
// intent.completed event into the outbox (so the webhook dispatcher
// signs and POSTs it to the merchant) and flips the intent into the
// completed terminal state. Both happen in the same transaction as
// the saga's own CompleteSagaRun update, so a successful commit means
// the merchant either has a webhook in flight or is guaranteed one.
type EmitCompleted struct {
	deps Deps
}

func NewEmitCompleted(deps Deps) *EmitCompleted { return &EmitCompleted{deps: deps} }

func (s *EmitCompleted) Name() string { return StepEmitCompleted }

// CompletedEvent is the payload merchants receive when their intent
// settles. Kept as a versioned struct so additive fields don't break
// receivers that parse with strict schemas.
type CompletedEvent struct {
	Schema       string `json:"schema"`
	IntentID     string `json:"intent_id"`
	MerchantID   string `json:"merchant_id"`
	ExternalRef  string `json:"external_ref"`
	State        string `json:"state"`
	AmountIDR    int64  `json:"amount_idr"`
	AmountUSDC   int64  `json:"quoted_amount_usdc"`
	OccurredAt   string `json:"occurred_at"`
}

func (s *EmitCompleted) Execute(ctx context.Context, tx pgx.Tx, run saga.Run) (saga.StepResult, error) {
	q := dbq.New(tx)
	intent, err := q.GetPaymentIntentByID(ctx, ldb.PgUUID(run.IntentID))
	if err != nil {
		return saga.StepResult{}, fmt.Errorf("emit_completed: load intent: %w", err)
	}

	updated := intent
	if intent.State == dbq.PaymentIntentStatePaidOut {
		row, err := q.UpdatePaymentIntentState(ctx, dbq.UpdatePaymentIntentStateParams{
			ID:    ldb.PgUUID(run.IntentID),
			State: dbq.PaymentIntentStateCompleted,
		})
		if err != nil {
			return saga.StepResult{}, fmt.Errorf("emit_completed: advance state: %w", err)
		}
		updated = row
	}

	merchantID := ldb.FromPgUUID(updated.MerchantID)
	intentID := ldb.FromPgUUID(updated.ID)

	event := CompletedEvent{
		Schema:      "selatpay.intent.completed.v1",
		IntentID:    intentID.String(),
		MerchantID:  merchantID.String(),
		ExternalRef: updated.ExternalRef,
		State:       string(updated.State),
		AmountIDR:   updated.AmountIdr,
		AmountUSDC:  updated.QuotedAmountUsdc,
		OccurredAt:  updated.UpdatedAt.Time.UTC().Format("2006-01-02T15:04:05.000Z07:00"),
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return saga.StepResult{}, fmt.Errorf("emit_completed: marshal event: %w", err)
	}

	headers := map[string]string{
		"X-Selatpay-Topic":           "intent.completed",
		"X-Selatpay-Idempotency-Key": fmt.Sprintf("intent.completed:%s", intentID),
	}
	if _, err := outbox.Publish(ctx, tx, "intent.completed", &intentID, payload, headers); err != nil {
		return saga.StepResult{}, fmt.Errorf("emit_completed: publish outbox: %w", err)
	}

	return saga.StepResult{}, nil
}
