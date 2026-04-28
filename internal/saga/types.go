package saga

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Kind names a saga family. Multiple kinds can coexist on the same
// intent over its lifetime (e.g., a settlement saga and a refund saga),
// each keyed independently by (intent_id, saga_kind).
type Kind string

const (
	// KindSettlement walks an intent from finalized deposit to merchant
	// payout and webhook notification.
	KindSettlement Kind = "intent_settlement"
)

// Run is the orchestrator-facing snapshot of a saga_runs row at the
// instant a step begins. Steps mutate ledger and intent state via the
// supplied transaction; they do not write back to saga_runs themselves —
// the runner advances saga state in the same transaction once the step
// returns successfully.
type Run struct {
	ID           uuid.UUID
	IntentID     uuid.UUID
	SagaKind     string
	CurrentStep  string
	StepAttempts int32
	LeaseOwner   string
	CreatedAt    time.Time
}

// StepResult tells the runner what to do next. NextStep is empty for the
// terminal step, in which case the runner marks the saga completed.
type StepResult struct {
	NextStep string
}

func (r StepResult) String() string {
	if r.NextStep == "" {
		return "result(terminal)"
	}
	return fmt.Sprintf("result(next=%s)", r.NextStep)
}

// Step is one node in a saga's state machine. Execute is invoked inside
// a Postgres transaction owned by the runner; the step performs all of
// its DB writes (ledger postings, intent transitions, outbox publishes)
// against tx so the saga advance commits atomically with the step's
// effects. A step must be idempotent: a failure between commit and the
// orchestrator updating saga state means the same step may run again on
// the next claim, and idempotency on external_ref / unique constraints
// is what gives the saga exactly-once *effect* on top of at-least-once
// execution.
type Step interface {
	Name() string
	Execute(ctx context.Context, tx pgx.Tx, run Run) (StepResult, error)
}

// Registry maps step names to implementations. Sagas reference steps by
// name in saga_runs.current_step, which keeps the schema decoupled from
// Go type identity and lets a deploy migrate a saga's path without a DB
// migration as long as the new step name resolves on startup.
type Registry struct {
	steps map[string]Step
}

func NewRegistry(steps ...Step) *Registry {
	r := &Registry{steps: make(map[string]Step, len(steps))}
	for _, s := range steps {
		r.steps[s.Name()] = s
	}
	return r
}

func (r *Registry) Get(name string) (Step, bool) {
	s, ok := r.steps[name]
	return s, ok
}

// terminalError flags a step error as non-retryable. The runner treats
// any other error as transient and reschedules the saga with backoff.
type terminalError struct{ err error }

func (e *terminalError) Error() string { return e.err.Error() }
func (e *terminalError) Unwrap() error { return e.err }

// Terminal wraps an error so the runner stops retrying and marks the
// saga failed. Steps use this for invariant violations that cannot be
// fixed by retry (missing intent, malformed state, exhausted business
// preconditions).
func Terminal(err error) error {
	if err == nil {
		return nil
	}
	return &terminalError{err: err}
}

func isTerminal(err error) bool {
	var t *terminalError
	return errors.As(err, &t)
}
