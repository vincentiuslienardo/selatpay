package idempotency

import (
	"bytes"
	"context"
	"crypto/subtle"
	"errors"
	"io"
	"net/http"

	"github.com/google/uuid"
)

// HeaderKey is the canonical request header clients set to opt into
// idempotent replay. Mirrors Stripe's convention.
const HeaderKey = "Idempotency-Key"

// MerchantResolver extracts the authenticated merchant from a request. The
// auth middleware sets it on the context; this keeps the idempotency package
// free of any auth dependency.
type MerchantResolver func(r *http.Request) (uuid.UUID, bool)

type Middleware struct {
	store    Store
	resolver MerchantResolver
}

func NewMiddleware(store Store, resolver MerchantResolver) *Middleware {
	return &Middleware{store: store, resolver: resolver}
}

// Handler wraps next. When an Idempotency-Key header is present and the
// request is a mutating method, the middleware either replays a prior
// response or captures this one for future replay.
func (m *Middleware) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isMutating(r.Method) {
			next.ServeHTTP(w, r)
			return
		}
		key := r.Header.Get(HeaderKey)
		if key == "" {
			next.ServeHTTP(w, r)
			return
		}
		merchantID, ok := m.resolver(r)
		if !ok {
			http.Error(w, "unauthenticated", http.StatusUnauthorized)
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
		_ = r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(body))

		hash := HashRequest(body)

		existing, err := m.store.Get(r.Context(), merchantID, key)
		switch {
		case err == nil:
			if subtle.ConstantTimeCompare(existing.RequestHash, hash) != 1 {
				http.Error(w, "idempotency key reused with a different request body", http.StatusConflict)
				return
			}
			w.Header().Set("Idempotent-Replay", "true")
			w.WriteHeader(existing.StatusCode)
			_, _ = w.Write(existing.ResponseBody)
			return
		case errors.Is(err, ErrNotFound):
			// proceed to capture
		default:
			http.Error(w, "idempotency lookup failed", http.StatusInternalServerError)
			return
		}

		cap := &captureWriter{ResponseWriter: w, status: http.StatusOK, buf: &bytes.Buffer{}}
		next.ServeHTTP(cap, r)

		if cap.status >= 500 {
			// Don't persist server errors — a retry should get a fresh attempt.
			return
		}

		rec := Record{
			MerchantID:   merchantID,
			Key:          key,
			RequestHash:  hash,
			StatusCode:   cap.status,
			ResponseBody: cap.buf.Bytes(),
		}

		stored, created, perr := m.store.Put(context.WithoutCancel(r.Context()), rec)
		if perr != nil {
			return
		}
		if !created && subtle.ConstantTimeCompare(stored.RequestHash, hash) != 1 {
			// Lost a race with a different-body writer. The stored record is
			// authoritative; what we just served was inconsistent. Nothing we
			// can do about the already-flushed response, but don't overwrite.
			return
		}
	})
}

func isMutating(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

type captureWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
	buf         *bytes.Buffer
}

func (c *captureWriter) WriteHeader(status int) {
	if c.wroteHeader {
		return
	}
	c.status = status
	c.wroteHeader = true
	c.ResponseWriter.WriteHeader(status)
}

func (c *captureWriter) Write(p []byte) (int, error) {
	if !c.wroteHeader {
		c.WriteHeader(http.StatusOK)
	}
	c.buf.Write(p)
	return c.ResponseWriter.Write(p)
}
