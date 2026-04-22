// Package wallet defines the Signer abstraction that hot-wallet signing has
// to satisfy. A local ed25519 implementation is shipped for dev and tests; a
// KMS-backed implementation is stubbed so the interface stays stable when a
// production signer is plugged in.
package wallet

import (
	"context"

	"github.com/gagliardetto/solana-go"
)

// Signer produces ed25519 signatures over raw message bytes. Implementations
// must be safe for concurrent use by multiple goroutines so callers can share
// one Signer across HTTP handlers, the orchestrator, and the payout rail.
type Signer interface {
	// PublicKey returns the ed25519 public key that would verify the output
	// of Sign. It is callable without side effects and must not return the
	// zero value once the Signer has been constructed successfully.
	PublicKey() solana.PublicKey

	// Sign returns an ed25519 signature over msg. A non-nil error indicates
	// the signing authority refused or could not reach its backing keystore;
	// callers must treat the returned Signature as invalid in that case.
	Sign(ctx context.Context, msg []byte) (solana.Signature, error)
}
