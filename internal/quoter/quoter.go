package quoter

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	dbq "github.com/vincentiuslienardo/selatpay/internal/db/sqlc"
)

// USDCDecimals matches the on-chain SPL mint metadata. IDR is a whole-unit
// currency (no sub-unit), so 1 IDR input → 1 integer unit.
const USDCDecimals = 6

var (
	ErrAmountNonPositive = errors.New("quoter: amount must be positive")
	ErrInvalidRate       = errors.New("quoter: rate must be positive")
	ErrInvalidSpread     = errors.New("quoter: spread_bps must be in [0, 10000]")
	ErrBadSignature      = errors.New("quoter: quote signature does not verify")
	ErrQuoteExpired      = errors.New("quoter: quote is expired")
)

type Quoter struct {
	pool       *pgxpool.Pool
	provider   Provider
	signingKey []byte
	ttl        time.Duration
	spreadBps  int32
	now        func() time.Time
}

type Options struct {
	TTL       time.Duration
	SpreadBps int32
	Now       func() time.Time
}

func New(pool *pgxpool.Pool, provider Provider, signingKey []byte, opts Options) *Quoter {
	if opts.TTL <= 0 {
		opts.TTL = 60 * time.Second
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Quoter{
		pool:       pool,
		provider:   provider,
		signingKey: signingKey,
		ttl:        opts.TTL,
		spreadBps:  opts.SpreadBps,
		now:        opts.Now,
	}
}

// Issue prices amountIDR against the provider rate for pair, applies the
// configured spread (against the customer), signs the quote, persists it,
// and returns it. The returned AmountUSDC is in USDC minor units (1e-6).
func (q *Quoter) Issue(ctx context.Context, pair string, amountIDR int64) (Quote, error) {
	if amountIDR <= 0 {
		return Quote{}, ErrAmountNonPositive
	}
	if q.spreadBps < 0 || q.spreadBps > 10000 {
		return Quote{}, ErrInvalidSpread
	}

	rate, err := q.provider.Rate(ctx, pair)
	if err != nil {
		return Quote{}, fmt.Errorf("fetch rate: %w", err)
	}
	if rate.Num <= 0 {
		return Quote{}, ErrInvalidRate
	}

	amountUSDC, err := QuoteAmount(amountIDR, rate, q.spreadBps)
	if err != nil {
		return Quote{}, err
	}

	now := q.now()
	expires := now.Add(q.ttl)

	qt := Quote{
		ID:         uuid.New(),
		Pair:       pair,
		Rate:       rate,
		SpreadBps:  q.spreadBps,
		ExpiresAt:  expires,
		AmountIDR:  amountIDR,
		AmountUSDC: amountUSDC,
		CreatedAt:  now,
	}
	qt.Signature = signQuote(q.signingKey, qt)

	if q.pool != nil {
		row, err := dbq.New(q.pool).CreateQuote(ctx, dbq.CreateQuoteParams{
			Pair:      pair,
			RateNum:   rate.Num,
			RateScale: rate.Scale,
			SpreadBps: q.spreadBps,
			ExpiresAt: pgtype.Timestamptz{Time: expires, Valid: true},
			Signature: qt.Signature,
		})
		if err != nil {
			return Quote{}, fmt.Errorf("persist quote: %w", err)
		}
		qt.ID = uuid.UUID(row.ID.Bytes)
		qt.CreatedAt = row.CreatedAt.Time
	}
	return qt, nil
}

// Verify re-signs a quote and checks expiry. Callers must call this before
// consuming a quote they received over the wire (or replayed from the DB).
func (q *Quoter) Verify(qt Quote) error {
	expected := signQuote(q.signingKey, qt)
	if !hmac.Equal(expected, qt.Signature) {
		return ErrBadSignature
	}
	if !qt.ExpiresAt.After(q.now()) {
		return ErrQuoteExpired
	}
	return nil
}

// QuoteAmount applies spread_bps against the customer and returns USDC minor
// units. Math is done in big.Int so no overflow on 1e18-scale Indonesian
// rupiah amounts, and no float.
//
// Given rate = "N rupiah per 1 USDC", effective rate after spread is
// N * (10000 - spread_bps) / 10000 (customer pays MORE USDC).
// amountUSDC_minor = amountIDR * 10^USDCDecimals / effective_rate.
func QuoteAmount(amountIDR int64, rate Rate, spreadBps int32) (int64, error) {
	if amountIDR <= 0 {
		return 0, ErrAmountNonPositive
	}
	if rate.Num <= 0 {
		return 0, ErrInvalidRate
	}
	if spreadBps < 0 || spreadBps > 10000 {
		return 0, ErrInvalidSpread
	}

	num := new(big.Int).SetInt64(amountIDR)
	num.Mul(num, big.NewInt(int64(pow10(int(USDCDecimals)))))
	num.Mul(num, big.NewInt(10000))

	// effective rate numerator = rate.Num * (10000 - spread_bps)
	effRate := new(big.Int).SetInt64(rate.Num)
	effRate.Mul(effRate, big.NewInt(int64(10000-spreadBps)))
	if rate.Scale > 0 {
		effRate.Mul(effRate, big.NewInt(int64(pow10(int(rate.Scale)))))
	}

	if effRate.Sign() == 0 {
		return 0, ErrInvalidRate
	}

	out := new(big.Int).Quo(num, effRate)
	if !out.IsInt64() {
		return 0, errors.New("quoter: computed amount overflows int64")
	}
	v := out.Int64()
	if v <= 0 {
		return 0, errors.New("quoter: computed amount rounds to zero")
	}
	return v, nil
}

func pow10(n int) int64 {
	r := int64(1)
	for i := 0; i < n; i++ {
		r *= 10
	}
	return r
}

func signQuote(key []byte, q Quote) []byte {
	mac := hmac.New(sha256.New, key)
	fmt.Fprintf(mac, "v1\n%s\n%d\n%d\n%d\n%d\n%d\n%d",
		q.Pair, q.Rate.Num, q.Rate.Scale, q.SpreadBps,
		q.AmountIDR, q.AmountUSDC, q.ExpiresAt.Unix())
	return mac.Sum(nil)
}
