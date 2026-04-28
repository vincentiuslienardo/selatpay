package saga

import (
	"math/rand/v2"
	"time"
)

// Backoff computes exponential delay with full jitter for the next saga
// attempt. The schedule starts at base, doubles each attempt up to max,
// and applies jitter in the range [0, candidate) so colocated workers
// don't synchronize their retries on a contended downstream.
type Backoff struct {
	Base time.Duration
	Max  time.Duration
}

func NewBackoff(base, max time.Duration) *Backoff {
	if base <= 0 {
		base = 500 * time.Millisecond
	}
	if max <= 0 || max < base {
		max = 5 * time.Minute
	}
	return &Backoff{
		Base: base,
		Max:  max,
	}
}

// Delay returns the wait before attempt number n (zero-indexed). The
// first retry uses Base; each subsequent doubles. Once the candidate
// exceeds Max it is clamped, then jitter is taken from [0, candidate).
// Jitter on the full window (not just the increment) is deliberate —
// it gives the strongest decorrelation across worker fleets and is what
// AWS recommends in their "exponential backoff and jitter" post.
//
// Uses math/rand/v2's package-level Int64N: jitter does not need
// cryptographic randomness, and v2's package functions are
// goroutine-safe and self-seeding so the Backoff struct doesn't have
// to carry a *rand.Rand.
func (b *Backoff) Delay(attempt int32) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	candidate := b.Base
	for i := int32(0); i < attempt && candidate < b.Max; i++ {
		candidate *= 2
	}
	if candidate > b.Max {
		candidate = b.Max
	}
	// Int64N requires n > 0; guard against the off-chance of a caller
	// setting Base to zero through reflection or test seams.
	if candidate <= 0 {
		return 0
	}
	// Jitter is decorrelation, not a security primitive — math/rand/v2 is
	// the right tool here.
	return time.Duration(rand.Int64N(int64(candidate))) //nolint:gosec // see comment above
}
