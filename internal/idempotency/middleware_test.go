package idempotency

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/google/uuid"
)

type memStore struct {
	mu   sync.Mutex
	data map[string]Record
}

func newMemStore() *memStore { return &memStore{data: make(map[string]Record)} }

func (m *memStore) key(mid uuid.UUID, k string) string { return mid.String() + "|" + k }

func (m *memStore) Get(_ context.Context, mid uuid.UUID, k string) (Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.data[m.key(mid, k)]; ok {
		return r, nil
	}
	return Record{}, ErrNotFound
}

func (m *memStore) Put(_ context.Context, r Record) (Record, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ck := m.key(r.MerchantID, r.Key)
	if existing, ok := m.data[ck]; ok {
		return existing, false, nil
	}
	m.data[ck] = r
	return r, true, nil
}

func newTestMiddleware(mid uuid.UUID) (*Middleware, *memStore) {
	s := newMemStore()
	mw := NewMiddleware(s, func(_ *http.Request) (uuid.UUID, bool) { return mid, true })
	return mw, s
}

func TestMiddleware_NoKey_PassesThrough(t *testing.T) {
	t.Parallel()
	mid := uuid.New()
	mw, store := newTestMiddleware(mid)

	calls := 0
	h := mw.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/intents", bytes.NewReader([]byte(`{}`)))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status: got %d", rec.Code)
	}
	if calls != 1 {
		t.Fatalf("handler calls: got %d, want 1", calls)
	}
	if len(store.data) != 0 {
		t.Fatalf("store should be empty without header")
	}
}

func TestMiddleware_GET_IsNotCaptured(t *testing.T) {
	t.Parallel()
	mid := uuid.New()
	mw, store := newTestMiddleware(mid)

	h := mw.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/v1/intents", nil)
	req.Header.Set(HeaderKey, "k1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if len(store.data) != 0 {
		t.Fatalf("GET requests must not be captured")
	}
}

func TestMiddleware_CapturesAndReplays(t *testing.T) {
	t.Parallel()
	mid := uuid.New()
	mw, _ := newTestMiddleware(mid)

	calls := 0
	h := mw.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		body, _ := io.ReadAll(r.Body)
		if !bytes.Equal(body, []byte(`{"amount":1000}`)) {
			t.Fatalf("handler saw wrong body: %s", body)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"abc"}`))
	}))

	do := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/intents", bytes.NewReader([]byte(`{"amount":1000}`)))
		req.Header.Set(HeaderKey, "k-replay")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	first := do()
	if first.Code != http.StatusCreated || first.Body.String() != `{"id":"abc"}` {
		t.Fatalf("first: code=%d body=%s", first.Code, first.Body.String())
	}
	if first.Header().Get("Idempotent-Replay") != "" {
		t.Fatalf("first call must not advertise replay")
	}

	second := do()
	if second.Code != http.StatusCreated || second.Body.String() != `{"id":"abc"}` {
		t.Fatalf("replay: code=%d body=%s", second.Code, second.Body.String())
	}
	if second.Header().Get("Idempotent-Replay") != "true" {
		t.Fatalf("replay must set Idempotent-Replay: true")
	}
	if calls != 1 {
		t.Fatalf("handler must run exactly once, ran %d times", calls)
	}
}

func TestMiddleware_HashMismatchReturnsConflict(t *testing.T) {
	t.Parallel()
	mid := uuid.New()
	mw, _ := newTestMiddleware(mid)

	h := mw.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))

	req1 := httptest.NewRequest(http.MethodPost, "/v1/intents", bytes.NewReader([]byte(`{"a":1}`)))
	req1.Header.Set(HeaderKey, "k-mismatch")
	h.ServeHTTP(httptest.NewRecorder(), req1)

	req2 := httptest.NewRequest(http.MethodPost, "/v1/intents", bytes.NewReader([]byte(`{"a":2}`)))
	req2.Header.Set(HeaderKey, "k-mismatch")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req2)

	if rec.Code != http.StatusConflict {
		t.Fatalf("mismatch: got %d, want 409", rec.Code)
	}
}

func TestMiddleware_5xx_NotPersisted(t *testing.T) {
	t.Parallel()
	mid := uuid.New()
	mw, store := newTestMiddleware(mid)

	h := mw.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`boom`))
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/intents", bytes.NewReader([]byte(`{}`)))
	req.Header.Set(HeaderKey, "k-5xx")
	h.ServeHTTP(httptest.NewRecorder(), req)

	if len(store.data) != 0 {
		t.Fatalf("5xx responses must not be persisted for replay")
	}
}

func TestMiddleware_UnauthenticatedReturns401(t *testing.T) {
	t.Parallel()
	store := newMemStore()
	mw := NewMiddleware(store, func(_ *http.Request) (uuid.UUID, bool) { return uuid.Nil, false })
	h := mw.Handler(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatalf("handler must not run for unauthenticated idempotent request")
	}))

	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte(`{}`)))
	req.Header.Set(HeaderKey, "k")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", rec.Code)
	}
}

var _ = errors.New // keep errors import if future assertions are added
