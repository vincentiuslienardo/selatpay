package steps

import (
	"log/slog"

	"github.com/vincentiuslienardo/selatpay/internal/ledger"
	"github.com/vincentiuslienardo/selatpay/internal/payout"
)

// Deps is the bag of collaborators that every step needs. Keeping this
// as a struct (not a constructor argument explosion) lets us wire new
// steps cheaply and lets tests inject a partial set without breaking
// the unrelated step factories.
type Deps struct {
	Ledger      *ledger.Ledger
	PayoutRails *payout.Router
	Log         *slog.Logger
}
