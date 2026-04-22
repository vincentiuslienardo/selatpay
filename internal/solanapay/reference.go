package solanapay

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"

	"github.com/gagliardetto/solana-go"
)

// referenceVersion is the leading byte of every sealed reference blob so
// the format can evolve (key rotation, cipher swap) without ambiguity. A
// future version would bump this and keep Reveal accepting both.
const referenceVersion byte = 0x01

// Allocator mints fresh ed25519 reference keypairs for Solana Pay intents
// and seals the private key with AES-256-GCM so it can be persisted in
// payment_intents.reference_secret_enc. The pubkey is attached to the
// transfer as a read-only account so the watcher can resolve the deposit
// via getSignaturesForAddress; the sealed secret exists in case ops ever
// needs to recover rent from the reference account.
type Allocator struct {
	aead cipher.AEAD
}

// NewAllocator wraps key as an AES-256-GCM AEAD. key must be exactly 32
// bytes; production reads it from SELATPAY_REFERENCE_ENC_KEY as hex.
func NewAllocator(key []byte) (*Allocator, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("solanapay: reference enc key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("solanapay: aes cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("solanapay: gcm: %w", err)
	}
	return &Allocator{aead: aead}, nil
}

// Reference is the output of Allocate. Pubkey is persisted in plaintext
// (and embedded in the Solana Pay URL); SecretEnc is persisted as bytea.
type Reference struct {
	Pubkey    solana.PublicKey
	SecretEnc []byte
}

// Allocate generates a fresh ed25519 keypair and returns the public key
// plus the AES-GCM-sealed private key. The sealed blob layout is:
//
//	version(1) | nonce(12) | ciphertext+tag
//
// A fresh nonce is sampled per call so two allocations of the same key
// never produce the same ciphertext (required for GCM security).
func (a *Allocator) Allocate() (Reference, error) {
	priv, err := solana.NewRandomPrivateKey()
	if err != nil {
		return Reference{}, fmt.Errorf("solanapay: generate keypair: %w", err)
	}
	blob, err := a.seal(priv)
	if err != nil {
		return Reference{}, err
	}
	return Reference{Pubkey: priv.PublicKey(), SecretEnc: blob}, nil
}

// Reveal decrypts a blob produced by Allocate. It returns an error if the
// version byte is unknown, the nonce is truncated, or the GCM tag fails to
// verify — the latter covers both tampering and wrong-key cases.
func (a *Allocator) Reveal(blob []byte) (solana.PrivateKey, error) {
	if len(blob) == 0 {
		return nil, errors.New("solanapay: empty ciphertext")
	}
	if blob[0] != referenceVersion {
		return nil, fmt.Errorf("solanapay: unsupported reference blob version 0x%02x", blob[0])
	}
	nonceSize := a.aead.NonceSize()
	if len(blob) < 1+nonceSize+a.aead.Overhead() {
		return nil, errors.New("solanapay: ciphertext too short")
	}
	nonce := blob[1 : 1+nonceSize]
	ct := blob[1+nonceSize:]
	pt, err := a.aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("solanapay: decrypt: %w", err)
	}
	priv := solana.PrivateKey(pt)
	if !priv.IsValid() {
		return nil, errors.New("solanapay: decrypted key is not a valid ed25519 private key")
	}
	return priv, nil
}

func (a *Allocator) seal(priv solana.PrivateKey) ([]byte, error) {
	nonceSize := a.aead.NonceSize()
	nonce := make([]byte, nonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("solanapay: nonce: %w", err)
	}
	ct := a.aead.Seal(nil, nonce, priv, nil)
	out := make([]byte, 0, 1+nonceSize+len(ct))
	out = append(out, referenceVersion)
	out = append(out, nonce...)
	out = append(out, ct...)
	return out, nil
}
