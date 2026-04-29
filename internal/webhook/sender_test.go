package webhook

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vincentiuslienardo/selatpay/internal/outbox"
)

// TestSign_DeterministicForFixedTimestamp pins the format so a
// receiver's verification harness can be authored against this.
func TestSign_DeterministicForFixedTimestamp(t *testing.T) {
	header, err := Sign([]byte("secret"), []byte("body"), time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix([]byte(header), []byte("t=1,v1=")) {
		t.Errorf("unexpected header shape: %s", header)
	}
}

// senderHarness wires a Sender against a httptest server but
// without a Postgres pool. Pool-backed paths are exercised in the
// integration test once it can run with testcontainers.
func mustSign(t *testing.T, secret, body []byte, ts time.Time) string {
	t.Helper()
	h, err := Sign(secret, body, ts)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return h
}

func TestSenderEndToEnd_HappyPathThroughHTTPTest(t *testing.T) {
	// We can't fully exercise Send without a Postgres pool, so this
	// test checks the same code path the Sender takes by hand: sign
	// the payload, POST to a httptest server, verify the receiver
	// sees the right signature. The Send() path is covered by the
	// integration test.
	secret := []byte("merchant-secret-bytes")
	body := []byte(`{"event":"intent.completed","intent_id":"abc"}`)
	ts := time.Unix(1_700_000_001, 0)

	var seen atomic.Pointer[http.Request]
	var seenBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Store(r)
		b, _ := io.ReadAll(r.Body)
		seenBody = b
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sigHeader := mustSign(t, secret, body, ts)
	req, _ := http.NewRequest(http.MethodPost, srv.URL, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(SignatureHeader, sigHeader)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	got := seen.Load()
	if got == nil {
		t.Fatal("no request observed")
	}
	if got.Header.Get(SignatureHeader) != sigHeader {
		t.Errorf("signature header missing or wrong")
	}
	if !bytes.Equal(seenBody, body) {
		t.Errorf("body mismatch")
	}

	// Receiver-side verification using the same secret reproduces
	// success, while a tampered body fails. This is the contract
	// merchant integrators will rely on.
	if err := Verify(secret, seenBody, sigHeader, ts, time.Minute); err != nil {
		t.Fatalf("verify happy: %v", err)
	}
	if err := Verify(secret, append(seenBody, '!'), sigHeader, ts, time.Minute); err == nil {
		t.Error("verify on tampered body should fail")
	}
}

func TestNewSender_RequiresPool(t *testing.T) {
	if _, err := NewSender(nil, Config{}); err == nil {
		t.Fatal("expected error for nil pool")
	}
}

// TestSendShape_NilHeadersIsNoOp confirms the Sender treats
// outbox messages without the merchant header as best-effort
// no-ops rather than retries, so a malformed publisher cannot
// stall the queue.
func TestSendShape_NilHeadersIsNoOp(t *testing.T) {
	// Construct a Sender with a nil pool indirectly by skipping
	// validation: we want to assert that the early-return path
	// in Send doesn't touch the pool when the merchant header is
	// missing. We exercise it by calling Send on a partially
	// initialised Sender; this is a lightweight assertion since
	// the full pool path is integration-tested.
	s := &Sender{
		log:   slog.Default(),
		clock: time.Now,
	}
	err := s.Send(context.Background(), outbox.Message{
		ID:      uuid.New(),
		Topic:   "intent.completed",
		Payload: []byte("{}"),
		Headers: map[string]string{},
	})
	if err != nil {
		t.Fatalf("missing merchant header should be a silent no-op, got %v", err)
	}
}
