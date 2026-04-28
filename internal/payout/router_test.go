package payout

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

type stubRail struct {
	name   string
	result SubmitResult
	err    error
}

func (s *stubRail) Name() string { return s.name }
func (s *stubRail) Submit(ctx context.Context, req SubmitRequest) (SubmitResult, error) {
	return s.result, s.err
}

func TestRouter_GetReturnsRegisteredRail(t *testing.T) {
	rail := &stubRail{name: "mock_idr_bank", result: SubmitResult{Outcome: OutcomeSuccess}}
	r := NewRouter(rail)
	got, err := r.Get("mock_idr_bank")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != rail {
		t.Errorf("got %v, want %v", got, rail)
	}
}

func TestRouter_GetUnknownRailReturnsErrUnknownRail(t *testing.T) {
	r := NewRouter()
	_, err := r.Get("nope")
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrUnknownRail) {
		t.Errorf("expected ErrUnknownRail, got %v", err)
	}
}

func TestRouter_NamesListsRegistered(t *testing.T) {
	r := NewRouter(&stubRail{name: "a"}, &stubRail{name: "b"})
	names := r.Names()
	if len(names) != 2 {
		t.Fatalf("got %d names, want 2", len(names))
	}
}

func TestRail_StubRoundTrip(t *testing.T) {
	rail := &stubRail{
		name: "mock",
		result: SubmitResult{
			Outcome:       OutcomeSuccess,
			RailReference: "ref-123",
			Message:       "ok",
		},
	}
	res, err := rail.Submit(context.Background(), SubmitRequest{
		PayoutID:    uuid.New(),
		IntentID:    uuid.New(),
		AmountIDR:   15000,
		Recipient:   Recipient{BankCode: "BCA", AccountNumber: "12345", AccountName: "ACME"},
		Idempotency: "idem-1",
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if res.Outcome != OutcomeSuccess {
		t.Errorf("outcome: got %v want success", res.Outcome)
	}
	if res.RailReference != "ref-123" {
		t.Errorf("ref: got %q", res.RailReference)
	}
}

func TestOutcome_String(t *testing.T) {
	cases := map[Outcome]string{
		OutcomeSuccess:   "success",
		OutcomeRetry:     "retry",
		OutcomePermanent: "permanent",
		Outcome(99):      "unknown",
	}
	for o, want := range cases {
		if got := o.String(); got != want {
			t.Errorf("Outcome(%d).String() = %q, want %q", o, got, want)
		}
	}
}
