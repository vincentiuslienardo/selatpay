package ledger

import "errors"

var (
	ErrNoLines            = errors.New("ledger: entry must have at least one posting line")
	ErrNonPositiveAmount  = errors.New("ledger: posting amount must be positive")
	ErrUnbalanced         = errors.New("ledger: entry is unbalanced (sum of debits must equal sum of credits per currency)")
	ErrInvalidDirection   = errors.New("ledger: direction must be debit or credit")
	ErrEmptyCurrency      = errors.New("ledger: posting currency must not be empty")
	ErrEmptyExternalRef   = errors.New("ledger: external_ref must not be empty")
	ErrEmptyKind          = errors.New("ledger: kind must not be empty")
	ErrEntryNotFound      = errors.New("ledger: journal entry not found")
	ErrAccountNotFound    = errors.New("ledger: account not found")
	ErrDuplicateExternalRef = errors.New("ledger: external_ref already exists")
)
