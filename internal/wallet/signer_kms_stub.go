package wallet

import (
	"context"
	"errors"

	"github.com/gagliardetto/solana-go"
)

// ErrKMSNotImplemented is the canonical error returned by KMSStubSigner so
// callers can differentiate "we chose not to wire a real KMS yet" from
// transient signing failures. ADR-005 describes the production design.
var ErrKMSNotImplemented = errors.New("wallet: kms signer not implemented (see ADR-005)")

// KMSStubSigner satisfies the Signer interface with a fixed public key but
// refuses to sign. It exists so the compose topology, DI wiring, and tests
// can exercise the Signer contract end-to-end without materialising a real
// private key, and so switching to a production signer is a constructor swap
// rather than a branch at every call site.
type KMSStubSigner struct {
	pub solana.PublicKey
}

// NewKMSStubSigner wraps a pre-provisioned KMS public key. The caller is
// expected to learn this value out-of-band (AWS KMS GetPublicKey, cloud
// console, etc.) since the stub never owns a private key.
func NewKMSStubSigner(pub solana.PublicKey) *KMSStubSigner {
	return &KMSStubSigner{pub: pub}
}

func (s *KMSStubSigner) PublicKey() solana.PublicKey { return s.pub }

func (s *KMSStubSigner) Sign(context.Context, []byte) (solana.Signature, error) {
	return solana.Signature{}, ErrKMSNotImplemented
}
