package wallet

import (
	"context"
	"errors"
	"testing"

	"github.com/gagliardetto/solana-go"
)

func TestLocalSigner_SignVerifyRoundTrip(t *testing.T) {
	priv, err := solana.NewRandomPrivateKey()
	if err != nil {
		t.Fatalf("NewRandomPrivateKey: %v", err)
	}
	s, err := NewLocalSignerFromPrivateKey(priv)
	if err != nil {
		t.Fatalf("NewLocalSignerFromPrivateKey: %v", err)
	}

	msg := []byte("selatpay local signer roundtrip")
	sig, err := s.Sign(context.Background(), msg)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if !s.PublicKey().Verify(msg, sig) {
		t.Fatal("signature failed to verify against own public key")
	}
	if s.PublicKey().Verify([]byte("mutated message"), sig) {
		t.Fatal("signature verified against a different message; ed25519 is broken")
	}
}

func TestLocalSigner_FromBase58MatchesSource(t *testing.T) {
	priv, err := solana.NewRandomPrivateKey()
	if err != nil {
		t.Fatalf("NewRandomPrivateKey: %v", err)
	}
	s, err := NewLocalSignerFromBase58(priv.String())
	if err != nil {
		t.Fatalf("NewLocalSignerFromBase58: %v", err)
	}
	if s.PublicKey() != priv.PublicKey() {
		t.Fatalf("pubkey drift: signer=%s source=%s", s.PublicKey(), priv.PublicKey())
	}
}

func TestLocalSigner_RejectsMalformedBase58(t *testing.T) {
	if _, err := NewLocalSignerFromBase58("not-a-real-base58-key"); err == nil {
		t.Fatal("expected error for invalid base58 secret")
	}
	if _, err := NewLocalSignerFromBase58(""); err == nil {
		t.Fatal("expected error for empty secret")
	}
}

func TestLocalSigner_RejectsInvalidPrivateKey(t *testing.T) {
	if _, err := NewLocalSignerFromPrivateKey(solana.PrivateKey{}); err == nil {
		t.Fatal("expected error for zero-valued private key")
	}
}

func TestKMSStubSigner_ReportsPublicKey(t *testing.T) {
	pub := solana.MustPublicKeyFromBase58("4zMMC9srt5Ri5X14GAgXhaHii3GnPAEERYPJgZJDncDU")
	s := NewKMSStubSigner(pub)
	if s.PublicKey() != pub {
		t.Fatalf("pubkey mismatch: got %s want %s", s.PublicKey(), pub)
	}
}

func TestKMSStubSigner_SignReturnsNotImplemented(t *testing.T) {
	s := NewKMSStubSigner(solana.MustPublicKeyFromBase58("4zMMC9srt5Ri5X14GAgXhaHii3GnPAEERYPJgZJDncDU"))
	_, err := s.Sign(context.Background(), []byte("whatever"))
	if !errors.Is(err, ErrKMSNotImplemented) {
		t.Fatalf("expected ErrKMSNotImplemented, got %v", err)
	}
}

// Interface sanity check: both implementations satisfy Signer.
var (
	_ Signer = (*LocalSigner)(nil)
	_ Signer = (*KMSStubSigner)(nil)
)
