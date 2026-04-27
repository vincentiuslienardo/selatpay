package saga

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
)

type fakeStep struct {
	name string
	exec func(context.Context, pgx.Tx, Run) (StepResult, error)
}

func (s *fakeStep) Name() string { return s.name }
func (s *fakeStep) Execute(ctx context.Context, tx pgx.Tx, run Run) (StepResult, error) {
	return s.exec(ctx, tx, run)
}

func TestRegistry_GetMissingReturnsFalse(t *testing.T) {
	r := NewRegistry()
	if _, ok := r.Get("nope"); ok {
		t.Fatal("expected missing step lookup to return ok=false")
	}
}

func TestRegistry_RoundTrip(t *testing.T) {
	want := &fakeStep{name: "credit_deposit"}
	r := NewRegistry(want)
	got, ok := r.Get("credit_deposit")
	if !ok {
		t.Fatal("expected step to be registered")
	}
	if got != want {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestTerminal_DetectableByIsTerminal(t *testing.T) {
	cause := errors.New("boom")
	if isTerminal(cause) {
		t.Fatal("plain error should not be terminal")
	}
	if !isTerminal(Terminal(cause)) {
		t.Fatal("Terminal(err) must be classified terminal")
	}
}

func TestTerminal_NilStaysNil(t *testing.T) {
	if got := Terminal(nil); got != nil {
		t.Fatalf("Terminal(nil) = %v, want nil", got)
	}
}
