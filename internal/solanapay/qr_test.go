package solanapay

import (
	"bytes"
	"testing"
)

// pngMagic is the 8-byte signature every valid PNG file begins with
// (ISO/IEC 15948). Matching the magic is a cheap sanity check that the
// encoder produced actual PNG data, not some other format.
var pngMagic = []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}

func TestQRPNG_EmitsValidPNG(t *testing.T) {
	img, err := QRPNG("solana:DjuMPGThkGdyk2vDvDDYjTUZynnBq9rZjYJBdoWcE7PG?amount=1", 320)
	if err != nil {
		t.Fatalf("QRPNG: %v", err)
	}
	if !bytes.HasPrefix(img, pngMagic) {
		t.Fatalf("output is not a PNG: first 16 bytes = %x", img[:16])
	}
	if len(img) < 100 {
		t.Fatalf("PNG suspiciously small: %d bytes", len(img))
	}
}

func TestQRPNG_RejectsEmptyURL(t *testing.T) {
	if _, err := QRPNG("", 320); err == nil {
		t.Fatal("expected error for empty URL")
	}
}

func TestQRPNG_RejectsNonPositiveSize(t *testing.T) {
	if _, err := QRPNG("solana:DjuMPGThkGdyk2vDvDDYjTUZynnBq9rZjYJBdoWcE7PG", 0); err == nil {
		t.Fatal("expected error for zero size")
	}
	if _, err := QRPNG("solana:DjuMPGThkGdyk2vDvDDYjTUZynnBq9rZjYJBdoWcE7PG", -10); err == nil {
		t.Fatal("expected error for negative size")
	}
}
