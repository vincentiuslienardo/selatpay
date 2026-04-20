package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	HeaderKeyID     = "X-Selatpay-Key-Id"
	HeaderTimestamp = "X-Selatpay-Timestamp"
	HeaderSignature = "X-Selatpay-Signature"

	// MaxClockSkew bounds how far a client's timestamp may drift from the
	// server's clock. 300s matches AWS SigV4 / Stripe webhook conventions.
	MaxClockSkew = 5 * time.Minute
)

var (
	ErrMissingHeaders  = errors.New("auth: missing required HMAC headers")
	ErrBadTimestamp    = errors.New("auth: timestamp unparseable or outside allowed clock skew")
	ErrUnknownKey      = errors.New("auth: unknown or revoked key id")
	ErrBadSignature    = errors.New("auth: signature does not match request")
	ErrBodyRead        = errors.New("auth: failed to read request body")
)

type ctxKey int

const (
	ctxMerchant ctxKey = iota
	ctxKeyID
)

// KeyStore resolves an X-Selatpay-Key-Id to the merchant and raw secret. The
// auth package is deliberately agnostic about where keys live; wire in a
// Postgres-backed impl at the edge.
type KeyStore interface {
	Lookup(ctx context.Context, keyID string) (merchantID uuid.UUID, secret []byte, err error)
}

// Middleware returns a net/http middleware that authenticates requests via
// HMAC-SHA256 and threads the merchant id onto the context.
func Middleware(store KeyStore, now func() time.Time) func(http.Handler) http.Handler {
	if now == nil {
		now = time.Now
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			keyID := r.Header.Get(HeaderKeyID)
			tsStr := r.Header.Get(HeaderTimestamp)
			sigHex := r.Header.Get(HeaderSignature)
			if keyID == "" || tsStr == "" || sigHex == "" {
				writeAuthError(w, http.StatusUnauthorized, "missing_credentials", ErrMissingHeaders.Error())
				return
			}

			ts, err := parseTimestamp(tsStr)
			if err != nil || absDuration(now().Sub(ts)) > MaxClockSkew {
				writeAuthError(w, http.StatusUnauthorized, "bad_timestamp", ErrBadTimestamp.Error())
				return
			}

			body, err := io.ReadAll(r.Body)
			if err != nil {
				writeAuthError(w, http.StatusBadRequest, "bad_body", ErrBodyRead.Error())
				return
			}
			_ = r.Body.Close()
			r.Body = io.NopCloser(strings.NewReader(string(body)))

			merchantID, secret, err := store.Lookup(r.Context(), keyID)
			if err != nil {
				writeAuthError(w, http.StatusUnauthorized, "unknown_key", ErrUnknownKey.Error())
				return
			}

			expected := Sign(secret, r.Method, r.URL.Path, tsStr, body)
			providedBytes, hexErr := hex.DecodeString(sigHex)
			if hexErr != nil || subtle.ConstantTimeCompare(expected, providedBytes) != 1 {
				writeAuthError(w, http.StatusUnauthorized, "bad_signature", ErrBadSignature.Error())
				return
			}

			ctx := context.WithValue(r.Context(), ctxMerchant, merchantID)
			ctx = context.WithValue(ctx, ctxKeyID, keyID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// Sign produces the canonical HMAC for a given request. Exported so tests and
// callers can compute signatures symmetrically.
func Sign(secret []byte, method, path, timestamp string, body []byte) []byte {
	bodyHash := sha256.Sum256(body)
	mac := hmac.New(sha256.New, secret)
	// canonical: TS \n METHOD \n PATH \n hex(sha256(body))
	fmt.Fprintf(mac, "%s\n%s\n%s\n%s", timestamp, method, path, hex.EncodeToString(bodyHash[:]))
	return mac.Sum(nil)
}

// MerchantFromContext returns the authenticated merchant id. The bool is false
// when the request has not been authenticated (e.g., public routes).
func MerchantFromContext(ctx context.Context) (uuid.UUID, bool) {
	v, ok := ctx.Value(ctxMerchant).(uuid.UUID)
	return v, ok
}

func KeyIDFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(ctxKeyID).(string)
	return v, ok
}

func parseTimestamp(raw string) (time.Time, error) {
	// Accept RFC3339 for human friendliness and Unix seconds for machine clients.
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t, nil
	}
	if secs, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return time.Unix(secs, 0), nil
	}
	return time.Time{}, errors.New("unparseable timestamp")
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

func writeAuthError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	fmt.Fprintf(w, `{"code":%q,"message":%q}`, code, msg)
}
