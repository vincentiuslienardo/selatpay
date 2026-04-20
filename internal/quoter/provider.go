package quoter

import (
	"context"
	"errors"
)

var ErrPairUnknown = errors.New("quoter: pair not supported by provider")

// Provider is the FX data source. Production would plug in Coingecko, Chainlink
// FX feeds, or a bank rate; the interface is tiny so swapping is trivial.
type Provider interface {
	// Rate returns the midmarket rate for base/quote. Callers apply spread.
	Rate(ctx context.Context, pair string) (Rate, error)
}

// MockProvider returns deterministic rates — good for tests and demo so we
// don't hit external APIs on a portfolio build.
type MockProvider struct {
	rates map[string]Rate
}

func NewMockProvider(rates map[string]Rate) *MockProvider {
	cp := make(map[string]Rate, len(rates))
	for k, v := range rates {
		cp[k] = v
	}
	return &MockProvider{rates: cp}
}

// DefaultMockRates is a representative snapshot for USDC/IDR, scaled at 0 (rate
// expressed as plain rupiah per 1 USDC).
func DefaultMockRates() map[string]Rate {
	return map[string]Rate{
		PairUSDCIDR: {Num: 16200, Scale: 0},
	}
}

func (m *MockProvider) Rate(_ context.Context, pair string) (Rate, error) {
	r, ok := m.rates[pair]
	if !ok {
		return Rate{}, ErrPairUnknown
	}
	return r, nil
}
