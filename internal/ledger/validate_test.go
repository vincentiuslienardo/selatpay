package ledger

import (
	"errors"
	"math/rand/v2"
	"testing"

	"github.com/google/uuid"
)

func TestValidate_Rejects(t *testing.T) {
	t.Parallel()
	acct := uuid.New()

	cases := []struct {
		name string
		e    Entry
		want error
	}{
		{
			name: "empty external_ref",
			e:    Entry{Kind: "k", Lines: []Line{{AccountID: acct, Amount: 1, Currency: "USD", Direction: Debit}}},
			want: ErrEmptyExternalRef,
		},
		{
			name: "empty kind",
			e:    Entry{ExternalRef: "r", Lines: []Line{{AccountID: acct, Amount: 1, Currency: "USD", Direction: Debit}}},
			want: ErrEmptyKind,
		},
		{
			name: "no lines",
			e:    Entry{ExternalRef: "r", Kind: "k"},
			want: ErrNoLines,
		},
		{
			name: "non-positive amount",
			e: Entry{ExternalRef: "r", Kind: "k", Lines: []Line{
				{AccountID: acct, Amount: 0, Currency: "USD", Direction: Debit},
				{AccountID: acct, Amount: 1, Currency: "USD", Direction: Credit},
			}},
			want: ErrNonPositiveAmount,
		},
		{
			name: "empty currency",
			e: Entry{ExternalRef: "r", Kind: "k", Lines: []Line{
				{AccountID: acct, Amount: 1, Currency: "", Direction: Debit},
				{AccountID: acct, Amount: 1, Currency: "", Direction: Credit},
			}},
			want: ErrEmptyCurrency,
		},
		{
			name: "invalid direction",
			e: Entry{ExternalRef: "r", Kind: "k", Lines: []Line{
				{AccountID: acct, Amount: 1, Currency: "USD", Direction: Direction("wat")},
			}},
			want: ErrInvalidDirection,
		},
		{
			name: "unbalanced single currency",
			e: Entry{ExternalRef: "r", Kind: "k", Lines: []Line{
				{AccountID: acct, Amount: 100, Currency: "USD", Direction: Debit},
				{AccountID: acct, Amount: 50, Currency: "USD", Direction: Credit},
			}},
			want: ErrUnbalanced,
		},
		{
			name: "unbalanced when one currency balances but another doesn't",
			e: Entry{ExternalRef: "r", Kind: "k", Lines: []Line{
				{AccountID: acct, Amount: 10, Currency: "USD", Direction: Debit},
				{AccountID: acct, Amount: 10, Currency: "USD", Direction: Credit},
				{AccountID: acct, Amount: 5, Currency: "IDR", Direction: Debit},
			}},
			want: ErrUnbalanced,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if err := Validate(c.e); !errors.Is(err, c.want) {
				t.Fatalf("got %v, want %v", err, c.want)
			}
		})
	}
}

func TestValidate_AcceptsBalanced(t *testing.T) {
	t.Parallel()
	acct := uuid.New()
	e := Entry{ExternalRef: "r", Kind: "k", Lines: []Line{
		{AccountID: acct, Amount: 1000, Currency: "USD", Direction: Debit},
		{AccountID: acct, Amount: 1000, Currency: "USD", Direction: Credit},
	}}
	if err := Validate(e); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
}

// Property: any random split of a total debit into N credit lines with the same
// total must validate. Uses math/rand/v2 for determinism via seeded source.
func TestValidate_Property_RandomBalancedSplits(t *testing.T) {
	t.Parallel()
	r := rand.New(rand.NewPCG(0xC0FFEE, 0xBEEF))
	acct := uuid.New()

	for i := 0; i < 500; i++ {
		n := 2 + r.IntN(8)
		total := int64(1 + r.IntN(1_000_000))

		lines := make([]Line, 0, n+1)
		lines = append(lines, Line{AccountID: acct, Amount: total, Currency: "USD", Direction: Debit})

		remaining := total
		for j := 0; j < n-1; j++ {
			if remaining <= 1 {
				break
			}
			part := int64(1 + r.IntN(int(remaining)))
			lines = append(lines, Line{AccountID: acct, Amount: part, Currency: "USD", Direction: Credit})
			remaining -= part
		}
		if remaining > 0 {
			lines = append(lines, Line{AccountID: acct, Amount: remaining, Currency: "USD", Direction: Credit})
		}

		e := Entry{ExternalRef: "r", Kind: "k", Lines: lines}
		if err := Validate(e); err != nil {
			t.Fatalf("iter %d: expected balanced, got %v (lines=%+v)", i, err, lines)
		}
	}
}

// Negative property: perturbing any balanced entry by +1 on one credit line
// must fail with ErrUnbalanced.
func TestValidate_Property_PerturbationBreaks(t *testing.T) {
	t.Parallel()
	r := rand.New(rand.NewPCG(0xFEED, 0xFACE))
	acct := uuid.New()

	for i := 0; i < 200; i++ {
		total := int64(100 + r.IntN(10_000))
		lines := []Line{
			{AccountID: acct, Amount: total, Currency: "USD", Direction: Debit},
			{AccountID: acct, Amount: total + 1, Currency: "USD", Direction: Credit},
		}
		e := Entry{ExternalRef: "r", Kind: "k", Lines: lines}
		if err := Validate(e); !errors.Is(err, ErrUnbalanced) {
			t.Fatalf("iter %d: want ErrUnbalanced, got %v", i, err)
		}
	}
}
