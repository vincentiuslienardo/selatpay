package solanapay

import (
	"bytes"
	"crypto/rand"
	"testing"
)

func randKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return k
}

func TestAllocator_AllocateRevealRoundTrip(t *testing.T) {
	a, err := NewAllocator(randKey(t))
	if err != nil {
		t.Fatalf("NewAllocator: %v", err)
	}
	ref, err := a.Allocate()
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	if ref.Pubkey.IsZero() {
		t.Fatal("allocated zero pubkey")
	}
	if len(ref.SecretEnc) == 0 {
		t.Fatal("empty sealed secret")
	}
	priv, err := a.Reveal(ref.SecretEnc)
	if err != nil {
		t.Fatalf("Reveal: %v", err)
	}
	if priv.PublicKey() != ref.Pubkey {
		t.Fatalf("pubkey drift after roundtrip: got %s want %s", priv.PublicKey(), ref.Pubkey)
	}
}

func TestAllocator_TwoAllocationsDiffer(t *testing.T) {
	a, _ := NewAllocator(randKey(t))
	r1, err := a.Allocate()
	if err != nil {
		t.Fatalf("Allocate r1: %v", err)
	}
	r2, err := a.Allocate()
	if err != nil {
		t.Fatalf("Allocate r2: %v", err)
	}
	if r1.Pubkey == r2.Pubkey {
		t.Fatal("two allocations returned the same pubkey")
	}
	if bytes.Equal(r1.SecretEnc, r2.SecretEnc) {
		t.Fatal("two allocations returned the same ciphertext — nonce reuse")
	}
}

func TestAllocator_RejectsTampering(t *testing.T) {
	a, _ := NewAllocator(randKey(t))
	ref, _ := a.Allocate()
	tampered := append([]byte{}, ref.SecretEnc...)
	tampered[len(tampered)-1] ^= 0xff
	if _, err := a.Reveal(tampered); err == nil {
		t.Fatal("Reveal accepted tampered ciphertext")
	}
}

func TestAllocator_RejectsWrongKey(t *testing.T) {
	a1, _ := NewAllocator(randKey(t))
	a2, _ := NewAllocator(randKey(t))
	ref, _ := a1.Allocate()
	if _, err := a2.Reveal(ref.SecretEnc); err == nil {
		t.Fatal("Reveal succeeded with wrong key")
	}
}

func TestAllocator_RejectsBadVersion(t *testing.T) {
	a, _ := NewAllocator(randKey(t))
	ref, _ := a.Allocate()
	ref.SecretEnc[0] = 0xff
	if _, err := a.Reveal(ref.SecretEnc); err == nil {
		t.Fatal("Reveal accepted bad version byte")
	}
}

func TestAllocator_RejectsShortBlob(t *testing.T) {
	a, _ := NewAllocator(randKey(t))
	if _, err := a.Reveal([]byte{referenceVersion}); err == nil {
		t.Fatal("Reveal accepted truncated blob")
	}
	if _, err := a.Reveal(nil); err == nil {
		t.Fatal("Reveal accepted empty blob")
	}
}

func TestNewAllocator_RejectsWrongKeySize(t *testing.T) {
	if _, err := NewAllocator(make([]byte, 16)); err == nil {
		t.Fatal("NewAllocator accepted 16-byte key")
	}
	if _, err := NewAllocator(nil); err == nil {
		t.Fatal("NewAllocator accepted nil key")
	}
	if _, err := NewAllocator(make([]byte, 64)); err == nil {
		t.Fatal("NewAllocator accepted 64-byte key")
	}
}
