// Package payout abstracts the merchant-facing fiat rails Selatpay
// settles into. The Rail interface lets the saga's trigger_payout step
// stay rail-agnostic; concrete rails (mock_idr_bank for dev, real bank
// integrations later) plug in via the Router.
package payout

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

// Outcome classifies a rail submission result. The saga step uses it
// to decide whether to advance, retry, or terminally fail; it is
// deliberately coarse so a new rail doesn't have to teach the saga
// about its specific status codes.
type Outcome int

const (
	// OutcomeSuccess: the rail accepted the payout and confirmed
	// settlement (or queued it for guaranteed delivery). The saga
	// books the IDR-side journal and advances toward completion.
	OutcomeSuccess Outcome = iota
	// OutcomeRetry: the rail returned a transient signal (5xx, network
	// error, timeout). The saga keeps the payout in 'submitting' state
	// and lets the runner reschedule with backoff. Idempotency keys
	// guarantee the next attempt is a no-op if the previous one
	// actually landed downstream.
	OutcomeRetry
	// OutcomePermanent: the rail rejected the payout for a reason no
	// retry will fix (account closed, KYC mismatch, amount exceeded
	// limits). The saga marks the intent failed and stops.
	OutcomePermanent
)

func (o Outcome) String() string {
	switch o {
	case OutcomeSuccess:
		return "success"
	case OutcomeRetry:
		return "retry"
	case OutcomePermanent:
		return "permanent"
	default:
		return "unknown"
	}
}

// Recipient holds the merchant-side bank coordinates the rail needs
// to disburse. The MVP carries a single Indonesian bank account
// shape; a real corridor expansion would parameterize this further
// (SWIFT, IBAN, virtual accounts) and let each rail surface what it
// requires.
type Recipient struct {
	BankCode      string
	AccountNumber string
	AccountName   string
}

// SubmitRequest is the rail-agnostic payout payload. Idempotency is
// the payout ID; rails MUST honor it so a saga retry does not produce
// a second downstream payment.
type SubmitRequest struct {
	PayoutID    uuid.UUID
	IntentID    uuid.UUID
	AmountIDR   int64
	Recipient   Recipient
	Memo        string
	Idempotency string
}

// SubmitResult is what a rail returns. RailReference is whatever the
// downstream calls its handle (transaction id, reference number); it
// gets persisted on payouts.rail_reference for recon.
type SubmitResult struct {
	Outcome       Outcome
	RailReference string
	Message       string
}

// Rail is the contract every payout destination must satisfy.
// Implementations must:
//   - Honor Idempotency: the same key MUST yield the same downstream
//     effect, regardless of how many times Submit is called.
//   - Be safe to call concurrently; the orchestrator may have
//     multiple workers per topic in larger deployments.
//   - Map transport-level concerns (timeouts, 5xx) to OutcomeRetry
//     and business-level rejects (4xx, validation errors) to
//     OutcomePermanent. Network errors that leave settlement state
//     ambiguous always go to OutcomeRetry — the idempotency key on
//     the rail side is what makes that safe.
type Rail interface {
	Name() string
	Submit(ctx context.Context, req SubmitRequest) (SubmitResult, error)
}

// ErrUnknownRail is returned by Router.Get when the requested rail
// name has no registered implementation. The saga step treats this
// as a deploy/config bug and fails the saga terminally, since no
// retry will introduce the missing rail.
var ErrUnknownRail = errors.New("payout: unknown rail")
