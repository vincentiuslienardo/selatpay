package config

import (
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	Env      string
	LogLevel string

	HTTPAddr      string
	DashboardAddr string

	DBURL     string
	RedisAddr string

	OTLPEndpoint string
	ServiceName  string

	SolanaRPCURL     string
	SolanaWSURL      string
	SolanaCommitment string
	USDCMint         string

	// HotWalletSecretBase58 is the Solana-format base58 secret (seed||pubkey)
	// that a LocalSigner loads for dev/test. Empty in production — KMS
	// deployments only know the public key out-of-band.
	HotWalletSecretBase58 string

	// HotWalletPubkey is the base58-encoded hot wallet public key, consulted
	// when HotWalletSecretBase58 is empty (e.g. a KMS-backed deployment).
	// At least one of the two must be set for the api subcommand.
	HotWalletPubkey string

	QuoteHMACSecret   []byte
	WebhookHMACSecret []byte
	APIKeyPepper      []byte

	// ReferenceEncKey is the 32-byte AES-256 key sealing Solana Pay reference
	// private keys at rest. Sourced from SELATPAY_REFERENCE_ENC_KEY as hex.
	ReferenceEncKey []byte

	// SolanaPayLabel and SolanaPayMessage are embedded in the generated
	// Solana Pay URL so the payer's wallet can show "Pay Selatpay — Order
	// 4242" rather than a bare destination. The message is a Sprintf-safe
	// template; empty values are omitted.
	SolanaPayLabel   string
	SolanaPayMessage string

	QuoteTTL       time.Duration
	QuoteSpreadBps int

	MockBankURL string

	HTTPReadTimeout  time.Duration
	HTTPWriteTimeout time.Duration
}

func Load() (Config, error) {
	refKey, err := decodeRefEncKey(os.Getenv("SELATPAY_REFERENCE_ENC_KEY"))
	if err != nil {
		return Config{}, err
	}
	c := Config{
		Env:                   getEnv("SELATPAY_ENV", "local"),
		LogLevel:              getEnv("SELATPAY_LOG_LEVEL", "info"),
		HTTPAddr:              getEnv("SELATPAY_HTTP_ADDR", ":8080"),
		DashboardAddr:         getEnv("SELATPAY_DASHBOARD_ADDR", ":8081"),
		DBURL:                 getEnv("SELATPAY_DB_URL", ""),
		RedisAddr:             getEnv("SELATPAY_REDIS_ADDR", "localhost:6379"),
		OTLPEndpoint:          getEnv("SELATPAY_OTEL_EXPORTER_OTLP_ENDPOINT", ""),
		ServiceName:           getEnv("SELATPAY_OTEL_SERVICE_NAME", "selatpayd"),
		SolanaRPCURL:          getEnv("SELATPAY_SOLANA_RPC_URL", "https://api.devnet.solana.com"),
		SolanaWSURL:           getEnv("SELATPAY_SOLANA_WS_URL", "wss://api.devnet.solana.com"),
		SolanaCommitment:      getEnv("SELATPAY_SOLANA_COMMITMENT", "finalized"),
		USDCMint:              getEnv("SELATPAY_USDC_MINT", "4zMMC9srt5Ri5X14GAgXhaHii3GnPAEERYPJgZJDncDU"),
		HotWalletSecretBase58: os.Getenv("SELATPAY_HOT_WALLET_SECRET_BASE58"),
		HotWalletPubkey:       os.Getenv("SELATPAY_HOT_WALLET_PUBKEY"),
		QuoteHMACSecret:       []byte(getEnv("SELATPAY_QUOTE_HMAC_SECRET", "")),
		WebhookHMACSecret:     []byte(getEnv("SELATPAY_WEBHOOK_HMAC_SECRET", "")),
		APIKeyPepper:          []byte(getEnv("SELATPAY_API_KEY_PEPPER", "")),
		ReferenceEncKey:       refKey,
		SolanaPayLabel:        getEnv("SELATPAY_SOLANA_PAY_LABEL", "Selatpay"),
		SolanaPayMessage:      getEnv("SELATPAY_SOLANA_PAY_MESSAGE", ""),
		QuoteTTL:              getDuration("SELATPAY_QUOTE_TTL", 60*time.Second),
		QuoteSpreadBps:        getInt("SELATPAY_QUOTE_SPREAD_BPS", 50),
		MockBankURL:           getEnv("SELATPAY_MOCK_BANK_URL", "http://localhost:9100"),
		HTTPReadTimeout:       getDuration("SELATPAY_HTTP_READ_TIMEOUT", 10*time.Second),
		HTTPWriteTimeout:      getDuration("SELATPAY_HTTP_WRITE_TIMEOUT", 30*time.Second),
	}
	if err := c.validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

func (c Config) validate() error {
	if c.DBURL == "" {
		return fmt.Errorf("SELATPAY_DB_URL is required")
	}
	if len(c.QuoteHMACSecret) == 0 {
		return fmt.Errorf("SELATPAY_QUOTE_HMAC_SECRET is required")
	}
	if len(c.WebhookHMACSecret) == 0 {
		return fmt.Errorf("SELATPAY_WEBHOOK_HMAC_SECRET is required")
	}
	if len(c.APIKeyPepper) == 0 {
		return fmt.Errorf("SELATPAY_API_KEY_PEPPER is required")
	}
	if len(c.ReferenceEncKey) != 32 {
		return fmt.Errorf("SELATPAY_REFERENCE_ENC_KEY must be 32 bytes of hex (64 chars)")
	}
	return nil
}

// decodeRefEncKey parses SELATPAY_REFERENCE_ENC_KEY as hex. An empty value is
// passed through so validate() can emit a uniform error; a malformed non-empty
// value fails fast so a misconfigured deploy doesn't silently fall back to a
// zero key.
func decodeRefEncKey(raw string) ([]byte, error) {
	if raw == "" {
		return nil, nil
	}
	k, err := hex.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("SELATPAY_REFERENCE_ENC_KEY is not valid hex: %w", err)
	}
	return k, nil
}

func getEnv(k, def string) string {
	if v, ok := os.LookupEnv(k); ok {
		return v
	}
	return def
}

func getDuration(k string, def time.Duration) time.Duration {
	v, ok := os.LookupEnv(k)
	if !ok {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}

func getInt(k string, def int) int {
	v, ok := os.LookupEnv(k)
	if !ok {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}
