// Package webhook owns the merchant-facing webhook signing and
// delivery contract. Producers (saga steps) hand events to the
// transactional outbox; this package is what actually signs and
// POSTs them when the dispatcher drains rows.
package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// SignatureHeader is the HTTP header carrying the timestamp and
// HMAC. Selatpay-Signature mirrors Stripe's pattern so receivers
// already familiar with that ecosystem can wire verification with
// minimal effort.
const SignatureHeader = "Selatpay-Signature"

// SignatureScheme labels the version of the signing format. Bumping
// to v2 in the future lets receivers stay backwards-compatible
// during a rotation window.
const SignatureScheme = "v1"

// DefaultTolerance is the maximum age a receiver should accept for a
// signed timestamp. Five minutes is the standard window: long enough
// to absorb clock skew and modest delivery delay, short enough to
// shrink the replay window.
const DefaultTolerance = 5 * time.Minute

// ErrEmptySecret guards callers against signing with a zero-length
// HMAC key, which would silently produce a deterministic but
// trivially forgeable signature.
var ErrEmptySecret = errors.New("webhook: signing secret is empty")

// Sign produces the canonical signature header value for body and
// timestamp. The signed bytes are timestamp.body (concatenated with
// a literal '.') so reordering or stripping either field invalidates
// the MAC. The header value is "t=<unix>,v1=<hex>".
//
// Receivers reconstruct the same bytes, recompute the MAC with the
// shared secret, and reject the request if either the MAC mismatches
// or the timestamp is outside the tolerance window.
func Sign(secret []byte, body []byte, ts time.Time) (string, error) {
	if len(secret) == 0 {
		return "", ErrEmptySecret
	}
	mac := hmac.New(sha256.New, secret)
	unix := ts.Unix()
	mac.Write([]byte(strconv.FormatInt(unix, 10)))
	mac.Write([]byte{'.'})
	mac.Write(body)
	sum := mac.Sum(nil)
	return fmt.Sprintf("t=%d,%s=%s", unix, SignatureScheme, hex.EncodeToString(sum)), nil
}

// Verify checks a signature header against body and the shared
// secret. now lets callers inject a clock for deterministic tests;
// in production it should be time.Now().
func Verify(secret []byte, body []byte, header string, now time.Time, tolerance time.Duration) error {
	if len(secret) == 0 {
		return ErrEmptySecret
	}
	ts, mac, err := parseSignatureHeader(header)
	if err != nil {
		return err
	}
	if tolerance > 0 {
		age := now.Sub(time.Unix(ts, 0))
		if age < 0 {
			age = -age
		}
		if age > tolerance {
			return fmt.Errorf("webhook: signature timestamp outside %s tolerance", tolerance)
		}
	}
	expected := hmac.New(sha256.New, secret)
	expected.Write([]byte(strconv.FormatInt(ts, 10)))
	expected.Write([]byte{'.'})
	expected.Write(body)
	if !hmac.Equal(mac, expected.Sum(nil)) {
		return errors.New("webhook: signature mismatch")
	}
	return nil
}

// parseSignatureHeader splits a "t=<unix>,v1=<hex>" header into its
// timestamp and MAC bytes. Unknown scheme tokens are ignored so the
// header can carry multiple v1/v2 values during a rotation, but at
// least one v1 entry must be present.
func parseSignatureHeader(s string) (int64, []byte, error) {
	if s == "" {
		return 0, nil, errors.New("webhook: empty signature header")
	}
	var ts int64
	var macHex string
	hasTS := false
	for _, part := range splitOn(s, ',') {
		part = trimSpace(part)
		eq := indexByte(part, '=')
		if eq < 0 {
			continue
		}
		key, val := part[:eq], part[eq+1:]
		switch key {
		case "t":
			n, err := strconv.ParseInt(val, 10, 64)
			if err != nil {
				return 0, nil, fmt.Errorf("webhook: bad timestamp: %w", err)
			}
			ts = n
			hasTS = true
		case SignatureScheme:
			macHex = val
		}
	}
	if !hasTS {
		return 0, nil, errors.New("webhook: missing t= in signature header")
	}
	if macHex == "" {
		return 0, nil, errors.New("webhook: missing v1= in signature header")
	}
	mac, err := hex.DecodeString(macHex)
	if err != nil {
		return 0, nil, fmt.Errorf("webhook: bad signature hex: %w", err)
	}
	return ts, mac, nil
}

// Tiny string helpers are kept inline so the package has no
// dependency surface beyond stdlib crypto/encoding.

func splitOn(s string, sep byte) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == sep {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}

func trimSpace(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}
