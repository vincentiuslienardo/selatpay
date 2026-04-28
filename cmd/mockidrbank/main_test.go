package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newTestServer() *httptest.Server {
	return httptest.NewServer(newServer(discardLogger()))
}

// post performs the request and returns just the parts the tests
// assert against, ensuring the response body is closed exactly once.
// Returning the *http.Response would force every caller to remember
// the defer; this shape sidesteps that and keeps each test small.
func post(t *testing.T, url, idemKey string, body any) (status int, respBody []byte) {
	t.Helper()
	buf, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(buf))
	req.Header.Set("Content-Type", "application/json")
	if idemKey != "" {
		req.Header.Set("X-Idempotency-Key", idemKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("http: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, _ = io.ReadAll(resp.Body)
	return resp.StatusCode, respBody
}

func TestPayouts_HappyPathReturnsReference(t *testing.T) {
	srv := newTestServer()
	defer srv.Close()

	status, body := post(t, srv.URL+"/payouts", "idem-1", submitRequest{
		PayoutID: "p-1", IntentID: "i-1", AmountIDR: 15000,
	})
	if status != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", status, body)
	}
	var parsed submitResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !strings.HasPrefix(parsed.Reference, "MIDR-") {
		t.Errorf("reference: got %q", parsed.Reference)
	}
	if parsed.Status != "succeeded" {
		t.Errorf("status: got %q", parsed.Status)
	}
}

func TestPayouts_FailRetryReturns503(t *testing.T) {
	srv := newTestServer()
	defer srv.Close()
	status, _ := post(t, srv.URL+"/payouts?fail=retry", "idem-r", submitRequest{PayoutID: "p", IntentID: "i", AmountIDR: 1})
	if status != http.StatusServiceUnavailable {
		t.Errorf("status: got %d want 503", status)
	}
}

func TestPayouts_FailPermanentReturns422(t *testing.T) {
	srv := newTestServer()
	defer srv.Close()
	status, _ := post(t, srv.URL+"/payouts?fail=permanent", "idem-p", submitRequest{PayoutID: "p", IntentID: "i", AmountIDR: 1})
	if status != http.StatusUnprocessableEntity {
		t.Errorf("status: got %d want 422", status)
	}
}

func TestPayouts_IdempotencyReplaysFirstResponse(t *testing.T) {
	srv := newTestServer()
	defer srv.Close()

	_, body1 := post(t, srv.URL+"/payouts", "idem-X", submitRequest{PayoutID: "a", IntentID: "i", AmountIDR: 1})
	// Second call uses the same idempotency key but a different
	// query string and body — the cache must replay the first
	// response anyway.
	status2, body2 := post(t, srv.URL+"/payouts?fail=permanent", "idem-X", submitRequest{PayoutID: "b", IntentID: "i2", AmountIDR: 9999})
	if status2 != http.StatusOK {
		t.Errorf("expected cached 200, got %d", status2)
	}
	if !bytes.Equal(body1, body2) {
		t.Errorf("cached body diverged: first=%s second=%s", body1, body2)
	}
}

func TestPayouts_RequiresIdempotencyKey(t *testing.T) {
	srv := newTestServer()
	defer srv.Close()
	status, _ := post(t, srv.URL+"/payouts", "", submitRequest{PayoutID: "a", IntentID: "i", AmountIDR: 1})
	if status != http.StatusBadRequest {
		t.Errorf("status: got %d want 400", status)
	}
}

func TestPayouts_RejectsNonPOST(t *testing.T) {
	srv := newTestServer()
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/payouts")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status: got %d want 405", resp.StatusCode)
	}
}

func TestHealthz_OK(t *testing.T) {
	srv := newTestServer()
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: %d", resp.StatusCode)
	}
}

func TestGetEnv_FallsBack(t *testing.T) {
	if got := getEnv("__MOCKBANK_TEST_VAR__", "fallback"); got != "fallback" {
		t.Errorf("got %q", got)
	}
	t.Setenv("__MOCKBANK_TEST_VAR__", "set")
	if got := getEnv("__MOCKBANK_TEST_VAR__", "fallback"); got != "set" {
		t.Errorf("got %q", got)
	}
	_ = os.Unsetenv("__MOCKBANK_TEST_VAR__")
}
