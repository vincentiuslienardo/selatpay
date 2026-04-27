package saga

import (
	"math/rand"
	"time"
)

// Backoff computes exponential delay with full jitter for the next saga
// attempt. The schedule starts at base, doubles each attempt up to max,
// and applies jitter in the range [0, candidate) so colocated workers
// don't synchronize their retries on a contended downstream.
type Backoff struct {
	Base time.Duration
	Max  time.Duration
	rng  *rand.Rand
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
		rng:  rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// Delay returns the wait before attempt number n (zero-indexed). The
// first retry uses Base; each subsequent doubles. Once the candidate
// exceeds Max it is clamped, then jitter is taken from [0, candidate).
// Jitter on the full window (not just the increment) is deliberate —
// it gives the strongest decorrelation across worker fleets and is what
// AWS recommends in their "exponential backoff and jitter" post.
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
	// rand.Int63n requires n > 0; guard against the off-chance of a
	// caller setting Base to zero through reflection or test seams.
	if candidate <= 0 {
		return 0
	}
	return time.Duration(b.rng.Int63n(int64(candidate)))
}
