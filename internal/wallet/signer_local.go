package wallet

import (
	"context"
	"errors"

	"github.com/gagliardetto/solana-go"
)

// LocalSigner holds an ed25519 private key in process memory and signs
// synchronously. It is appropriate for dev, CI, and solana-test-validator
// integration tests; production deployments should swap in a KMS-backed
// Signer that never materialises the private key locally.
type LocalSigner struct {
	priv solana.PrivateKey
	pub  solana.PublicKey
}

// NewLocalSignerFromBase58 parses a Solana-format base58 secret key (the
// "secretKey" produced by solana-keygen) and returns a LocalSigner. The input
// is an ed25519 full private key, i.e. 64 bytes: seed || pubkey.
func NewLocalSignerFromBase58(secret string) (*LocalSigner, error) {
	priv, err := solana.PrivateKeyFromBase58(secret)
	if err != nil {
		return nil, err
	}
	return NewLocalSignerFromPrivateKey(priv)
}

// NewLocalSignerFromPrivateKey is the constructor used by tests that already
// have a solana.PrivateKey in hand (e.g. solana.NewRandomPrivateKey).
func NewLocalSignerFromPrivateKey(priv solana.PrivateKey) (*LocalSigner, error) {
	if !priv.IsValid() {
		return nil, errors.New("wallet: ed25519 private key is not valid")
	}
	return &LocalSigner{priv: priv, pub: priv.PublicKey()}, nil
}

func (s *LocalSigner) PublicKey() solana.PublicKey { return s.pub }

func (s *LocalSigner) Sign(_ context.Context, msg []byte) (solana.Signature, error) {
	return s.priv.Sign(msg)
}
