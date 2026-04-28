// Package solanapay implements the Solana Pay primitives Selatpay needs:
// transfer-request URL build/parse, reference-keypair allocation with
// encryption at rest, and QR rendering. It does not talk to the chain — it
// produces the strings and payloads the payer's wallet and the chain
// watcher consume.
package solanapay

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/gagliardetto/solana-go"
)

// URLParams is the structured form of a Solana Pay transfer-request URL
// (https://docs.solanapay.com/spec#transfer-request). Fields map to the
// URL's query component; Recipient becomes the opaque part after the
// "solana:" scheme.
type URLParams struct {
	// Recipient is the transfer destination. For SPL transfers this is the
	// owner wallet, not the destination token account — wallets resolve the
	// ATA themselves from (Recipient, SPLToken).
	Recipient solana.PublicKey

	// SPLToken is the SPL mint for token transfers. Nil means native SOL.
	SPLToken *solana.PublicKey

	// Amount is a decimal string in display units (e.g. "1.5" for 1.5 USDC).
	// Empty means the wallet prompts the user for an amount. Callers that
	// have a raw base-unit integer should run it through FormatAmount first.
	Amount string

	// References are read-only accounts attached to the transfer. Selatpay
	// emits exactly one per intent so the watcher can resolve a deposit via
	// getSignaturesForAddress against the reference pubkey.
	References []solana.PublicKey

	Label   string // requester display name shown by wallet
	Message string // human-readable context line shown by wallet
	Memo    string // on-chain memo attached via the Memo program
}

// ErrInvalidURL signals a malformed transfer-request URL. Callers that need
// finer-grained errors should inspect the wrapped cause via errors.Unwrap.
var ErrInvalidURL = errors.New("solanapay: invalid transfer-request URL")

// BuildURL renders p as a "solana:" transfer-request URL. Query parameters
// are emitted in url.Values.Encode's sorted order so the output is stable
// across runs, which keeps test assertions and log diffing straightforward.
func BuildURL(p URLParams) (string, error) {
	if p.Recipient.IsZero() {
		return "", errors.New("solanapay: recipient public key is required")
	}
	u := url.URL{
		Scheme: "solana",
		Opaque: p.Recipient.String(),
	}
	q := url.Values{}
	if p.Amount != "" {
		q.Set("amount", p.Amount)
	}
	if p.SPLToken != nil {
		if p.SPLToken.IsZero() {
			return "", errors.New("solanapay: spl-token mint is zero")
		}
		q.Set("spl-token", p.SPLToken.String())
	}
	for _, r := range p.References {
		if r.IsZero() {
			return "", errors.New("solanapay: reference public key is zero")
		}
		q.Add("reference", r.String())
	}
	if p.Label != "" {
		q.Set("label", p.Label)
	}
	if p.Message != "" {
		q.Set("message", p.Message)
	}
	if p.Memo != "" {
		q.Set("memo", p.Memo)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// ParseURL is the inverse of BuildURL. It validates the scheme, decodes the
// recipient, and extracts query parameters. It accepts zero or more
// reference values (Solana Pay allows the key to repeat).
func ParseURL(s string) (URLParams, error) {
	if s == "" {
		return URLParams{}, ErrInvalidURL
	}
	u, err := url.Parse(s)
	if err != nil {
		return URLParams{}, fmt.Errorf("%w: %w", ErrInvalidURL, err)
	}
	if u.Scheme != "solana" {
		return URLParams{}, fmt.Errorf("%w: scheme %q", ErrInvalidURL, u.Scheme)
	}
	if u.Opaque == "" {
		return URLParams{}, fmt.Errorf("%w: missing recipient", ErrInvalidURL)
	}
	recipient, err := solana.PublicKeyFromBase58(u.Opaque)
	if err != nil {
		return URLParams{}, fmt.Errorf("%w: recipient: %w", ErrInvalidURL, err)
	}
	q := u.Query()
	out := URLParams{
		Recipient: recipient,
		Amount:    q.Get("amount"),
		Label:     q.Get("label"),
		Message:   q.Get("message"),
		Memo:      q.Get("memo"),
	}
	if splStr := q.Get("spl-token"); splStr != "" {
		mint, err := solana.PublicKeyFromBase58(splStr)
		if err != nil {
			return URLParams{}, fmt.Errorf("%w: spl-token: %w", ErrInvalidURL, err)
		}
		out.SPLToken = &mint
	}
	for _, refStr := range q["reference"] {
		ref, err := solana.PublicKeyFromBase58(refStr)
		if err != nil {
			return URLParams{}, fmt.Errorf("%w: reference: %w", ErrInvalidURL, err)
		}
		out.References = append(out.References, ref)
	}
	return out, nil
}

// FormatAmount renders raw as a decimal string with `decimals` fractional
// places, trimming trailing zeros so 1.500000 becomes "1.5". That matches
// the form wallet UIs display and keeps the URL short.
func FormatAmount(raw uint64, decimals uint8) string {
	if decimals == 0 {
		return strconv.FormatUint(raw, 10)
	}
	divisor := pow10(decimals)
	whole := raw / divisor
	frac := raw % divisor
	if frac == 0 {
		return strconv.FormatUint(whole, 10)
	}
	fracStr := fmt.Sprintf("%0*d", decimals, frac)
	fracStr = strings.TrimRight(fracStr, "0")
	return fmt.Sprintf("%d.%s", whole, fracStr)
}

// ParseAmount inverts FormatAmount. It rejects exponential notation, signs,
// and fractional parts longer than `decimals` — silent truncation at the
// smallest representable unit would hide real rounding bugs.
func ParseAmount(s string, decimals uint8) (uint64, error) {
	if s == "" {
		return 0, errors.New("solanapay: empty amount")
	}
	if strings.ContainsAny(s, "eE") {
		return 0, fmt.Errorf("solanapay: exponential notation not supported: %q", s)
	}
	parts := strings.SplitN(s, ".", 2)
	whole, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("solanapay: parse whole part: %w", err)
	}
	multiplier := pow10(decimals)
	if multiplier > 0 && whole > ^uint64(0)/multiplier {
		return 0, fmt.Errorf("solanapay: amount overflow: %q", s)
	}
	result := whole * multiplier
	if len(parts) == 2 {
		frac := parts[1]
		if len(frac) > int(decimals) {
			return 0, fmt.Errorf("solanapay: fractional part %q exceeds %d decimals", frac, decimals)
		}
		// Right-pad to exactly `decimals` digits so "5" with 6 decimals
		// becomes 500000, not 5.
		for len(frac) < int(decimals) {
			frac += "0"
		}
		f, err := strconv.ParseUint(frac, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("solanapay: parse fractional part: %w", err)
		}
		result += f
	}
	return result, nil
}

func pow10(n uint8) uint64 {
	var r uint64 = 1
	for i := uint8(0); i < n; i++ {
		r *= 10
	}
	return r
}
