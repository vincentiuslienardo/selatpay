package auth

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
)

type staticStore struct {
	keyID      string
	merchantID uuid.UUID
	secret     []byte
}

func (s staticStore) Lookup(_ context.Context, keyID string) (uuid.UUID, []byte, error) {
	if keyID != s.keyID {
		return uuid.Nil, nil, ErrUnknownKey
	}
	return s.merchantID, s.secret, nil
}

func sign(t *testing.T, secret []byte, ts string, req *http.Request, body []byte) string {
	t.Helper()
	return hex.EncodeToString(Sign(secret, req.Method, req.URL.Path, ts, body))
}

func signedRequest(t *testing.T, store staticStore, now time.Time, body []byte) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/payment_intents", bytes.NewReader(body))
	ts := strconv.FormatInt(now.Unix(), 10)
	req.Header.Set(HeaderKeyID, store.keyID)
	req.Header.Set(HeaderTimestamp, ts)
	req.Header.Set(HeaderSignature, sign(t, store.secret, ts, req, body))
	return req
}

func TestMiddleware_SignedRequestPasses(t *testing.T) {
	t.Parallel()
	store := staticStore{keyID: "k-live", merchantID: uuid.New(), secret: []byte("topsecret")}
	now := time.Unix(1_700_000_000, 0)

	called := false
	var seenMerchant uuid.UUID
	mw := Middleware(store, func() time.Time { return now })
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		seenMerchant, _ = MerchantFromContext(r.Context())
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"a":1}` {
			t.Fatalf("handler did not see original body, got %s", body)
		}
		w.WriteHeader(http.StatusOK)
	}))

	req := signedRequest(t, store, now, []byte(`{"a":1}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || !called {
		t.Fatalf("code=%d called=%v body=%s", rec.Code, called, rec.Body.String())
	}
	if seenMerchant != store.merchantID {
		t.Fatalf("merchant id not propagated to handler context")
	}
}

func TestMiddleware_RejectsTamperedBody(t *testing.T) {
	t.Parallel()
	store := staticStore{keyID: "k", merchantID: uuid.New(), secret: []byte("s")}
	now := time.Unix(1_700_000_000, 0)
	mw := Middleware(store, func() time.Time { return now })
	h := mw(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatalf("tampered request must not reach handler")
	}))

	req := signedRequest(t, store, now, []byte(`{"a":1}`))
	// Tamper with the body after signing.
	req.Body = io.NopCloser(bytes.NewReader([]byte(`{"a":2}`)))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", rec.Code)
	}
	var body map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["code"] != "bad_signature" {
		t.Fatalf("unexpected error code: %v", body)
	}
}

func TestMiddleware_RejectsStaleTimestamp(t *testing.T) {
	t.Parallel()
	store := staticStore{keyID: "k", merchantID: uuid.New(), secret: []byte("s")}
	now := time.Unix(1_700_000_000, 0)
	mw := Middleware(store, func() time.Time { return now.Add(10 * time.Minute) })
	h := mw(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatalf("stale request must not reach handler")
	}))

	req := signedRequest(t, store, now, []byte(`{}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401 for stale timestamp", rec.Code)
	}
}

func TestMiddleware_RejectsUnknownKey(t *testing.T) {
	t.Parallel()
	store := staticStore{keyID: "known", merchantID: uuid.New(), secret: []byte("s")}
	now := time.Unix(1_700_000_000, 0)
	mw := Middleware(store, func() time.Time { return now })
	h := mw(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatalf("unknown-key request must not reach handler")
	}))

	body := []byte(`{}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/payment_intents", bytes.NewReader(body))
	ts := strconv.FormatInt(now.Unix(), 10)
	req.Header.Set(HeaderKeyID, "unknown")
	req.Header.Set(HeaderTimestamp, ts)
	req.Header.Set(HeaderSignature, sign(t, store.secret, ts, req, body))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", rec.Code)
	}
}

func TestMiddleware_MissingHeadersReturns401(t *testing.T) {
	t.Parallel()
	store := staticStore{keyID: "k", merchantID: uuid.New(), secret: []byte("s")}
	mw := Middleware(store, time.Now)
	h := mw(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))

	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(nil))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", rec.Code)
	}
}

func TestDeriveSecret_Deterministic(t *testing.T) {
	t.Parallel()
	a := DeriveSecret([]byte("pepper"), []byte("raw"))
	b := DeriveSecret([]byte("pepper"), []byte("raw"))
	if !bytes.Equal(a, b) {
		t.Fatalf("DeriveSecret must be deterministic")
	}
	c := DeriveSecret([]byte("pepper2"), []byte("raw"))
	if bytes.Equal(a, c) {
		t.Fatalf("different pepper must produce different derived key")
	}
}
