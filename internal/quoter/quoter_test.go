package quoter

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestQuoteAmount_NoSpread(t *testing.T) {
	t.Parallel()
	// 1 USDC = 16200 IDR, no spread.
	// 16_200_000 IDR → 1_000 USDC → 1_000 * 1e6 = 1_000_000_000 minor units.
	got, err := QuoteAmount(16_200_000, Rate{Num: 16200, Scale: 0}, 0)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != 1_000_000_000 {
		t.Fatalf("got %d, want 1_000_000_000", got)
	}
}

func TestQuoteAmount_SpreadIncreasesUSDCCost(t *testing.T) {
	t.Parallel()
	base, err := QuoteAmount(16_200_000, Rate{Num: 16200, Scale: 0}, 0)
	if err != nil {
		t.Fatal(err)
	}
	withSpread, err := QuoteAmount(16_200_000, Rate{Num: 16200, Scale: 0}, 50) // 50 bps
	if err != nil {
		t.Fatal(err)
	}
	if withSpread <= base {
		t.Fatalf("spread against customer must increase USDC cost; base=%d withSpread=%d", base, withSpread)
	}
}

func TestQuoteAmount_Rejects(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		amount int64
		rate   Rate
		spread int32
		want   error
	}{
		{"zero amount", 0, Rate{Num: 16200}, 0, ErrAmountNonPositive},
		{"negative amount", -1, Rate{Num: 16200}, 0, ErrAmountNonPositive},
		{"zero rate", 100, Rate{Num: 0}, 0, ErrInvalidRate},
		{"spread too high", 100, Rate{Num: 16200}, 10001, ErrInvalidSpread},
		{"negative spread", 100, Rate{Num: 16200}, -1, ErrInvalidSpread},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			_, err := QuoteAmount(c.amount, c.rate, c.spread)
			if !errors.Is(err, c.want) {
				t.Fatalf("got %v, want %v", err, c.want)
			}
		})
	}
}

func TestIssue_SignedAndVerifiable(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_700_000_000, 0).UTC()
	q := New(nil, NewMockProvider(DefaultMockRates()), []byte("sk"), Options{
		TTL:       time.Minute,
		SpreadBps: 50,
		Now:       func() time.Time { return now },
	})

	qt, err := q.Issue(context.Background(), PairUSDCIDR, 16_200_000)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if qt.AmountUSDC == 0 {
		t.Fatalf("expected nonzero USDC")
	}
	if qt.ExpiresAt != now.Add(time.Minute) {
		t.Fatalf("ttl wrong: %v", qt.ExpiresAt)
	}
	if err := q.Verify(qt); err != nil {
		t.Fatalf("verify on fresh quote: %v", err)
	}

	// Tamper: bumping amount must break the signature.
	bad := qt
	bad.AmountUSDC = qt.AmountUSDC - 1
	if err := q.Verify(bad); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("tampered verify: got %v, want ErrBadSignature", err)
	}
}

func TestVerify_Expired(t *testing.T) {
	t.Parallel()
	start := time.Unix(1_700_000_000, 0)
	var clock = start
	q := New(nil, NewMockProvider(DefaultMockRates()), []byte("sk"), Options{
		TTL:       time.Second,
		SpreadBps: 0,
		Now:       func() time.Time { return clock },
	})
	qt, err := q.Issue(context.Background(), PairUSDCIDR, 1_000)
	if err != nil {
		t.Fatal(err)
	}
	clock = start.Add(2 * time.Second)
	if err := q.Verify(qt); !errors.Is(err, ErrQuoteExpired) {
		t.Fatalf("got %v, want ErrQuoteExpired", err)
	}
}

func TestProvider_UnknownPair(t *testing.T) {
	t.Parallel()
	p := NewMockProvider(DefaultMockRates())
	if _, err := p.Rate(context.Background(), "BTC/IDR"); !errors.Is(err, ErrPairUnknown) {
		t.Fatalf("got %v, want ErrPairUnknown", err)
	}
}
