// Package rails contains the concrete payout.Rail implementations.
// MockIDRBank is the dev/test rail; production rails (xendit, flip,
// wise — when those are integrated) live as siblings.
package rails

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/vincentiuslienardo/selatpay/internal/payout"
)

// MockIDRBankName is the rail key persisted on payouts.rail. The
// orchestrator registers MockIDRBank under this name; saga steps
// look it up by string.
const MockIDRBankName = "mock_idr_bank"

// MockIDRBank submits payouts to the standalone mock bank server in
// the compose stack. The server speaks one POST /payouts endpoint and
// honors the X-Idempotency-Key header so a retry never produces a
// second disbursement; status code maps cleanly to payout.Outcome.
type MockIDRBank struct {
	baseURL string
	client  *http.Client
}

// NewMockIDRBank returns a rail talking to baseURL. Pass an explicit
// http.Client to override the default 10s timeout (e.g., tests using
// httptest with a tighter window).
func NewMockIDRBank(baseURL string, client *http.Client) *MockIDRBank {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &MockIDRBank{baseURL: baseURL, client: client}
}

func (m *MockIDRBank) Name() string { return MockIDRBankName }

type submitBody struct {
	IntentID      string `json:"intent_id"`
	PayoutID      string `json:"payout_id"`
	AmountIDR     int64  `json:"amount_idr"`
	BankCode      string `json:"bank_code"`
	AccountNumber string `json:"account_number"`
	AccountName   string `json:"account_name"`
	Memo          string `json:"memo,omitempty"`
}

type submitResponse struct {
	Reference string `json:"reference"`
	Status    string `json:"status"`
	Message   string `json:"message,omitempty"`
}

// Submit POSTs the request to the mock bank. Status code mapping:
//   - 200/201 → OutcomeSuccess
//   - 422/400 → OutcomePermanent (validation rejects the saga can't
//     fix by retrying)
//   - 503/504/5xx → OutcomeRetry
//   - Any transport error (timeout, refused) → OutcomeRetry, since
//     idempotency-key dedup makes a second attempt safe.
func (m *MockIDRBank) Submit(ctx context.Context, req payout.SubmitRequest) (payout.SubmitResult, error) {
	body, err := json.Marshal(submitBody{
		IntentID:      req.IntentID.String(),
		PayoutID:      req.PayoutID.String(),
		AmountIDR:     req.AmountIDR,
		BankCode:      req.Recipient.BankCode,
		AccountNumber: req.Recipient.AccountNumber,
		AccountName:   req.Recipient.AccountName,
		Memo:          req.Memo,
	})
	if err != nil {
		return payout.SubmitResult{}, fmt.Errorf("mock_idr_bank: marshal body: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, m.baseURL+"/payouts", bytes.NewReader(body))
	if err != nil {
		return payout.SubmitResult{}, fmt.Errorf("mock_idr_bank: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Idempotency-Key", req.Idempotency)

	resp, err := m.client.Do(httpReq)
	if err != nil {
		// Network-layer failures are always retryable: the request may or
		// may not have landed, and the idempotency key on the bank side
		// makes a second attempt a no-op if it did.
		return payout.SubmitResult{
			Outcome: payout.OutcomeRetry,
			Message: err.Error(),
		}, nil
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return payout.SubmitResult{Outcome: payout.OutcomeRetry, Message: "read body: " + err.Error()}, nil
	}

	var parsed submitResponse
	if len(raw) > 0 {
		// Unparseable body still maps cleanly via status code; keep
		// the raw bytes available in Message for ops to inspect.
		_ = json.Unmarshal(raw, &parsed)
	}

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return payout.SubmitResult{
			Outcome:       payout.OutcomeSuccess,
			RailReference: parsed.Reference,
			Message:       parsed.Message,
		}, nil
	case resp.StatusCode == http.StatusUnprocessableEntity || resp.StatusCode == http.StatusBadRequest:
		return payout.SubmitResult{
			Outcome: payout.OutcomePermanent,
			Message: pickMessage(parsed.Message, raw, resp.StatusCode),
		}, nil
	case resp.StatusCode >= 500:
		return payout.SubmitResult{
			Outcome: payout.OutcomeRetry,
			Message: pickMessage(parsed.Message, raw, resp.StatusCode),
		}, nil
	default:
		// 401/403/404/etc are unexpected from the mock bank — surface as
		// permanent so ops investigates rather than the saga loops.
		return payout.SubmitResult{
			Outcome: payout.OutcomePermanent,
			Message: pickMessage(parsed.Message, raw, resp.StatusCode),
		}, nil
	}
}

func pickMessage(parsed string, raw []byte, status int) string {
	if parsed != "" {
		return parsed
	}
	if len(raw) > 0 {
		return fmt.Sprintf("%d: %s", status, string(raw))
	}
	return fmt.Sprintf("status %d", status)
}

// ErrInvalidBaseURL is returned when configuration produces a malformed
// or empty URL for the mock bank. Surface this at orchestrator boot so
// a misconfigured deploy fails fast rather than at first payout.
var ErrInvalidBaseURL = errors.New("mock_idr_bank: base URL is required")

// Validate is a small helper the orchestrator can call before
// registering the rail with the router; it lets boot-time errors be
// reported cleanly with the rest of the config validation.
func Validate(baseURL string) error {
	if baseURL == "" {
		return ErrInvalidBaseURL
	}
	return nil
}
