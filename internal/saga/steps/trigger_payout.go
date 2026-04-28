package steps

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	ldb "github.com/vincentiuslienardo/selatpay/internal/db"
	dbq "github.com/vincentiuslienardo/selatpay/internal/db/sqlc"
	"github.com/vincentiuslienardo/selatpay/internal/payout"
	"github.com/vincentiuslienardo/selatpay/internal/payout/rails"
	"github.com/vincentiuslienardo/selatpay/internal/saga"
)

// TriggerPayout is the saga step that submits the IDR-side payout to
// the configured rail. It is idempotent in two layers: the payouts
// table is keyed on intent_id (so a saga retry reuses the same row)
// and the rail is contracted to honor SubmitRequest.Idempotency (so
// the bank-side dedup makes a re-submission a no-op if the previous
// one actually landed). Outcome from the rail decides whether the
// saga advances, retries, or terminally fails.
type TriggerPayout struct {
	deps Deps
}

func NewTriggerPayout(deps Deps) *TriggerPayout { return &TriggerPayout{deps: deps} }

func (s *TriggerPayout) Name() string { return StepTriggerPayout }

func (s *TriggerPayout) Execute(ctx context.Context, tx pgx.Tx, run saga.Run) (saga.StepResult, error) {
	q := dbq.New(tx)

	intent, err := q.GetPaymentIntentByID(ctx, ldb.PgUUID(run.IntentID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return saga.StepResult{}, saga.Terminal(fmt.Errorf("trigger_payout: intent %s not found", run.IntentID))
		}
		return saga.StepResult{}, fmt.Errorf("trigger_payout: load intent: %w", err)
	}

	merchant, err := q.GetMerchantByID(ctx, intent.MerchantID)
	if err != nil {
		return saga.StepResult{}, fmt.Errorf("trigger_payout: load merchant: %w", err)
	}
	recipient, err := recipientFromMerchant(merchant)
	if err != nil {
		// Missing bank details is a configuration problem an operator
		// must resolve manually; no retry will introduce them.
		return saga.StepResult{}, saga.Terminal(fmt.Errorf("trigger_payout: %w", err))
	}

	// Idempotent: returns existing row on replay.
	po, err := q.UpsertPayout(ctx, dbq.UpsertPayoutParams{
		IntentID:  ldb.PgUUID(run.IntentID),
		Rail:      rails.MockIDRBankName,
		AmountIdr: intent.AmountIdr,
	})
	if err != nil {
		return saga.StepResult{}, fmt.Errorf("trigger_payout: upsert payout: %w", err)
	}

	// Already-resolved payouts skip the rail call entirely and let
	// apply_payout_result do its accounting work. This makes the
	// step replay-safe even after the rail returned succeeded but
	// the saga advance never committed.
	switch po.State {
	case dbq.PayoutStateSucceeded:
		return s.advanceIntentToSettling(ctx, q, intent)
	case dbq.PayoutStateFailed:
		return saga.StepResult{}, saga.Terminal(fmt.Errorf("trigger_payout: payout already failed: %s", deref(po.LastError)))
	}

	rail, err := s.deps.PayoutRails.Get(po.Rail)
	if err != nil {
		// Unknown rail at saga time means a deploy/config bug — no
		// retry adds the missing rail.
		return saga.StepResult{}, saga.Terminal(fmt.Errorf("trigger_payout: %w", err))
	}

	if _, err := q.MarkPayoutSubmitting(ctx, po.ID); err != nil {
		return saga.StepResult{}, fmt.Errorf("trigger_payout: mark submitting: %w", err)
	}

	result, err := rail.Submit(ctx, payout.SubmitRequest{
		PayoutID:    ldb.FromPgUUID(po.ID),
		IntentID:    run.IntentID,
		AmountIDR:   po.AmountIdr,
		Recipient:   recipient,
		Memo:        fmt.Sprintf("selatpay/%s", intent.ExternalRef),
		Idempotency: ldb.FromPgUUID(po.ID).String(),
	})
	if err != nil {
		// A concrete error from the rail is unexpected — the contract
		// asks rails to map their failures into an Outcome. Treat as
		// retryable on the saga side; the next attempt with the same
		// idempotency key is a no-op if the original landed.
		return saga.StepResult{}, fmt.Errorf("trigger_payout: rail submit: %w", err)
	}

	switch result.Outcome {
	case payout.OutcomeSuccess:
		if _, err := q.MarkPayoutSucceeded(ctx, dbq.MarkPayoutSucceededParams{
			ID:            po.ID,
			RailReference: nullableString(result.RailReference),
		}); err != nil {
			return saga.StepResult{}, fmt.Errorf("trigger_payout: mark succeeded: %w", err)
		}
		return s.advanceIntentToSettling(ctx, q, intent)

	case payout.OutcomeRetry:
		errMsg := fmt.Sprintf("rail retry: %s", result.Message)
		if _, err := q.ResetPayoutToPending(ctx, dbq.ResetPayoutToPendingParams{
			ID:        po.ID,
			LastError: nullableString(errMsg),
		}); err != nil {
			return saga.StepResult{}, fmt.Errorf("trigger_payout: reset pending: %w", err)
		}
		// Returning a non-Terminal error lets the runner reschedule
		// with backoff; the same payout row sits in 'pending' until
		// a future attempt closes it out.
		return saga.StepResult{}, errors.New(errMsg)

	case payout.OutcomePermanent:
		errMsg := result.Message
		if errMsg == "" {
			errMsg = "rail permanently rejected payout"
		}
		if _, err := q.MarkPayoutFailed(ctx, dbq.MarkPayoutFailedParams{
			ID:        po.ID,
			LastError: nullableString(errMsg),
		}); err != nil {
			return saga.StepResult{}, fmt.Errorf("trigger_payout: mark failed: %w", err)
		}
		// TODO(phase-8): post a compensation entry that backs out
		// the deposit credit so the books reflect the held USDC
		// going to a refund queue rather than a still-owed merchant
		// liability. For now, the intent fails and ops resolves
		// off-ledger.
		if _, err := q.UpdatePaymentIntentState(ctx, dbq.UpdatePaymentIntentStateParams{
			ID:    ldb.PgUUID(run.IntentID),
			State: dbq.PaymentIntentStateFailed,
		}); err != nil {
			return saga.StepResult{}, fmt.Errorf("trigger_payout: fail intent: %w", err)
		}
		return saga.StepResult{}, saga.Terminal(errors.New(errMsg))

	default:
		return saga.StepResult{}, saga.Terminal(fmt.Errorf("trigger_payout: unknown outcome %v", result.Outcome))
	}
}

func (s *TriggerPayout) advanceIntentToSettling(ctx context.Context, q *dbq.Queries, intent dbq.PaymentIntent) (saga.StepResult, error) {
	if intent.State == dbq.PaymentIntentStateFunded {
		if _, err := q.UpdatePaymentIntentState(ctx, dbq.UpdatePaymentIntentStateParams{
			ID:    intent.ID,
			State: dbq.PaymentIntentStateSettling,
		}); err != nil {
			return saga.StepResult{}, fmt.Errorf("trigger_payout: advance state: %w", err)
		}
	}
	return saga.StepResult{NextStep: StepApplyPayoutResult}, nil
}

func recipientFromMerchant(m dbq.Merchant) (payout.Recipient, error) {
	if m.BankCode == nil || *m.BankCode == "" {
		return payout.Recipient{}, fmt.Errorf("merchant %s missing bank_code", ldb.FromPgUUID(m.ID))
	}
	if m.BankAccountNumber == nil || *m.BankAccountNumber == "" {
		return payout.Recipient{}, fmt.Errorf("merchant %s missing bank_account_number", ldb.FromPgUUID(m.ID))
	}
	if m.BankAccountName == nil || *m.BankAccountName == "" {
		return payout.Recipient{}, fmt.Errorf("merchant %s missing bank_account_name", ldb.FromPgUUID(m.ID))
	}
	return payout.Recipient{
		BankCode:      *m.BankCode,
		AccountNumber: *m.BankAccountNumber,
		AccountName:   *m.BankAccountName,
	}, nil
}

func nullableString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
