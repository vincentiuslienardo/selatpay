package rails

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/vincentiuslienardo/selatpay/internal/payout"
)

func TestMockIDRBank_Submit_SuccessMaps200ToSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method: %s", r.Method)
		}
		if r.Header.Get("X-Idempotency-Key") != "idem-1" {
			t.Errorf("idempotency key not forwarded: %q", r.Header.Get("X-Idempotency-Key"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(submitResponse{Reference: "ref-1", Status: "succeeded"})
	}))
	defer srv.Close()

	rail := NewMockIDRBank(srv.URL, srv.Client())
	res, err := rail.Submit(context.Background(), payout.SubmitRequest{
		PayoutID:    uuid.New(),
		IntentID:    uuid.New(),
		AmountIDR:   15000,
		Idempotency: "idem-1",
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if res.Outcome != payout.OutcomeSuccess {
		t.Errorf("outcome: got %v want success", res.Outcome)
	}
	if res.RailReference != "ref-1" {
		t.Errorf("ref: got %q", res.RailReference)
	}
}

func TestMockIDRBank_Submit_5xxMapsToRetry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	rail := NewMockIDRBank(srv.URL, srv.Client())
	res, err := rail.Submit(context.Background(), payout.SubmitRequest{
		PayoutID: uuid.New(), IntentID: uuid.New(), AmountIDR: 1, Idempotency: "k",
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if res.Outcome != payout.OutcomeRetry {
		t.Errorf("outcome: got %v want retry", res.Outcome)
	}
}

func TestMockIDRBank_Submit_422MapsToPermanent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_ = json.NewEncoder(w).Encode(submitResponse{Status: "rejected", Message: "account closed"})
	}))
	defer srv.Close()

	rail := NewMockIDRBank(srv.URL, srv.Client())
	res, err := rail.Submit(context.Background(), payout.SubmitRequest{
		PayoutID: uuid.New(), IntentID: uuid.New(), AmountIDR: 1, Idempotency: "k",
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if res.Outcome != payout.OutcomePermanent {
		t.Errorf("outcome: got %v want permanent", res.Outcome)
	}
	if res.Message != "account closed" {
		t.Errorf("message: got %q", res.Message)
	}
}

func TestMockIDRBank_Submit_TransportErrorMapsToRetry(t *testing.T) {
	// Point at a port nothing is listening on; the request fails at
	// the dial step and must surface as Retry, not an error.
	rail := NewMockIDRBank("http://127.0.0.1:1", &http.Client{})
	res, err := rail.Submit(context.Background(), payout.SubmitRequest{
		PayoutID: uuid.New(), IntentID: uuid.New(), AmountIDR: 1, Idempotency: "k",
	})
	if err != nil {
		t.Fatalf("submit returned error (should map to Retry instead): %v", err)
	}
	if res.Outcome != payout.OutcomeRetry {
		t.Errorf("outcome: got %v want retry", res.Outcome)
	}
}

func TestValidate_RejectsEmpty(t *testing.T) {
	if err := Validate(""); err == nil {
		t.Fatal("expected error for empty URL")
	}
	if err := Validate("http://localhost:9100"); err != nil {
		t.Errorf("expected nil for valid URL, got %v", err)
	}
}
