// Package steps holds the concrete saga step implementations referenced
// by name in saga_runs.current_step. Steps are deliberately single-file
// where their bodies are short; the package boundary keeps the runner
// (which knows nothing about ledger or chain semantics) decoupled from
// business logic that depends on the rest of the platform.
package steps

import (
	"github.com/vincentiuslienardo/selatpay/internal/saga"
)

// Step name constants. The saga state machine references steps by
// these string keys; renaming a constant requires renaming saga_runs
// rows that point at it, so prefer additive changes.
const (
	StepCreditDeposit     = "credit_deposit"
	StepTriggerPayout     = "trigger_payout"
	StepApplyPayoutResult = "apply_payout_result"
	StepEmitCompleted     = "emit_completed"
)

// FirstStep is the entry point for a fresh settlement saga. The
// watcher enqueues a saga_run with this as current_step the moment a
// finalized deposit is linked to an intent.
const FirstStep = StepCreditDeposit

// All returns every settlement step in registration order. Helper for
// the orchestrator wiring so the subcommand harness doesn't have to
// list each step manually.
func All(deps Deps) []saga.Step {
	return []saga.Step{
		NewCreditDeposit(deps),
		NewTriggerPayout(deps),
		NewApplyPayoutResult(deps),
		NewEmitCompleted(deps),
	}
}
