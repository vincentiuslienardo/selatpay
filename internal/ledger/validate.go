package ledger

import "strings"

// Validate runs the in-process checks on an Entry. The database trigger is the
// authoritative enforcer, but pre-validating here gives callers a fast, typed
// error without a round-trip.
func Validate(e Entry) error {
	if strings.TrimSpace(e.ExternalRef) == "" {
		return ErrEmptyExternalRef
	}
	if strings.TrimSpace(e.Kind) == "" {
		return ErrEmptyKind
	}
	if len(e.Lines) == 0 {
		return ErrNoLines
	}
	sums := make(map[string]int64)
	for _, l := range e.Lines {
		if l.Amount <= 0 {
			return ErrNonPositiveAmount
		}
		if strings.TrimSpace(l.Currency) == "" {
			return ErrEmptyCurrency
		}
		switch l.Direction {
		case Debit:
			sums[l.Currency] += l.Amount
		case Credit:
			sums[l.Currency] -= l.Amount
		default:
			return ErrInvalidDirection
		}
	}
	for _, diff := range sums {
		if diff != 0 {
			return ErrUnbalanced
		}
	}
	return nil
}
