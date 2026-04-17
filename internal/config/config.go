package config

import (
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

	HotWalletSecretBase58 string
	QuoteHMACSecret       []byte
	WebhookHMACSecret     []byte

	MockBankURL string

	HTTPReadTimeout  time.Duration
	HTTPWriteTimeout time.Duration
}

func Load() (Config, error) {
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
		QuoteHMACSecret:       []byte(getEnv("SELATPAY_QUOTE_HMAC_SECRET", "")),
		WebhookHMACSecret:     []byte(getEnv("SELATPAY_WEBHOOK_HMAC_SECRET", "")),
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
	return nil
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

//nolint:unused // reserved for future int env vars
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
