package config

import (
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("SELATPAY_DB_URL", "postgres://x@y/z")
	t.Setenv("SELATPAY_QUOTE_HMAC_SECRET", "qsecret")
	t.Setenv("SELATPAY_WEBHOOK_HMAC_SECRET", "wsecret")
	t.Setenv("SELATPAY_API_KEY_PEPPER", "pepper")
	t.Setenv("SELATPAY_REFERENCE_ENC_KEY", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.HTTPAddr != ":8080" {
		t.Errorf("HTTPAddr default: got %q", c.HTTPAddr)
	}
	if c.SolanaCommitment != "finalized" {
		t.Errorf("SolanaCommitment default: got %q", c.SolanaCommitment)
	}
	if c.HTTPReadTimeout != 10*time.Second {
		t.Errorf("HTTPReadTimeout default: got %v", c.HTTPReadTimeout)
	}
	if c.USDCMint == "" {
		t.Errorf("USDCMint should default to Circle devnet mint")
	}
}

func TestLoadRequiresDB(t *testing.T) {
	t.Setenv("SELATPAY_DB_URL", "")
	t.Setenv("SELATPAY_QUOTE_HMAC_SECRET", "x")
	t.Setenv("SELATPAY_WEBHOOK_HMAC_SECRET", "y")
	t.Setenv("SELATPAY_API_KEY_PEPPER", "p")

	if _, err := Load(); err == nil {
		t.Fatal("expected error when DB URL is empty")
	}
}

func TestLoadRequiresHMACSecrets(t *testing.T) {
	t.Setenv("SELATPAY_DB_URL", "postgres://x@y/z")
	t.Setenv("SELATPAY_QUOTE_HMAC_SECRET", "")
	t.Setenv("SELATPAY_WEBHOOK_HMAC_SECRET", "y")
	t.Setenv("SELATPAY_API_KEY_PEPPER", "p")

	if _, err := Load(); err == nil {
		t.Fatal("expected error when quote HMAC secret is empty")
	}
}

func TestLoadRequiresAPIKeyPepper(t *testing.T) {
	t.Setenv("SELATPAY_DB_URL", "postgres://x@y/z")
	t.Setenv("SELATPAY_QUOTE_HMAC_SECRET", "x")
	t.Setenv("SELATPAY_WEBHOOK_HMAC_SECRET", "y")
	t.Setenv("SELATPAY_API_KEY_PEPPER", "")
	t.Setenv("SELATPAY_REFERENCE_ENC_KEY", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")

	if _, err := Load(); err == nil {
		t.Fatal("expected error when API key pepper is empty")
	}
}

func TestLoadRequiresReferenceEncKey(t *testing.T) {
	t.Setenv("SELATPAY_DB_URL", "postgres://x@y/z")
	t.Setenv("SELATPAY_QUOTE_HMAC_SECRET", "x")
	t.Setenv("SELATPAY_WEBHOOK_HMAC_SECRET", "y")
	t.Setenv("SELATPAY_API_KEY_PEPPER", "p")
	t.Setenv("SELATPAY_REFERENCE_ENC_KEY", "")

	if _, err := Load(); err == nil {
		t.Fatal("expected error when reference enc key is empty")
	}
}

func TestLoadRejectsShortReferenceEncKey(t *testing.T) {
	t.Setenv("SELATPAY_DB_URL", "postgres://x@y/z")
	t.Setenv("SELATPAY_QUOTE_HMAC_SECRET", "x")
	t.Setenv("SELATPAY_WEBHOOK_HMAC_SECRET", "y")
	t.Setenv("SELATPAY_API_KEY_PEPPER", "p")
	// 16 hex chars = 8 bytes, too short for AES-256.
	t.Setenv("SELATPAY_REFERENCE_ENC_KEY", "0123456789abcdef")

	if _, err := Load(); err == nil {
		t.Fatal("expected error when reference enc key is too short")
	}
}

func TestLoadRejectsNonHexReferenceEncKey(t *testing.T) {
	t.Setenv("SELATPAY_DB_URL", "postgres://x@y/z")
	t.Setenv("SELATPAY_QUOTE_HMAC_SECRET", "x")
	t.Setenv("SELATPAY_WEBHOOK_HMAC_SECRET", "y")
	t.Setenv("SELATPAY_API_KEY_PEPPER", "p")
	t.Setenv("SELATPAY_REFERENCE_ENC_KEY", "not-hex-at-all")

	if _, err := Load(); err == nil {
		t.Fatal("expected error when reference enc key is not hex")
	}
}
