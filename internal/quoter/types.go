package quoter

import (
	"time"

	"github.com/google/uuid"
)

// Pair is the canonical label for an FX pair. Quotes are always quoted as
// "base/quote" where the rate expresses how many units of quote per 1 base.
//
// For SEA remittance the inbound pair is USDC/IDR — "one USDC buys N rupiah".
const PairUSDCIDR = "USDC/IDR"

// Rate is an exact rational, stored as (num / 10^scale). We never round-trip
// through float for settlement math; this keeps the quote engine and the
// ledger numerically consistent.
type Rate struct {
	Num   int64
	Scale int16
}

// Quote is a signed promise that a given USDC amount will settle a given IDR
// request if the payer funds within ExpiresAt. Signature is HMAC-SHA256 over
// the canonical serialization and is re-verified at intent-consumption time.
type Quote struct {
	ID          uuid.UUID
	Pair        string
	Rate        Rate
	SpreadBps   int32
	ExpiresAt   time.Time
	Signature   []byte
	AmountIDR   int64 // request input
	AmountUSDC  int64 // derived using rate + spread, in USDC minor units (1e-6)
	CreatedAt   time.Time
}
