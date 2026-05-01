// Command seed provisions a demo merchant, an api key, and a webhook
// configuration in the local Selatpay database. Output is a single
// JSON object with the values the demo orchestration needs (merchant
// id, key id, derived secret, webhook receiver path).
//
// This tool is for the demo only; production deployments would use a
// real merchant onboarding flow and never have raw secrets in stdout.
//
// Usage:
//
//	go run ./scripts/seed \
//	    --pg-url postgres://selatpay:selatpay@localhost:5432/selatpay?sslmode=disable \
//	    --pepper devsecret-api-key-pepper-rotate-me \
//	    --merchant-name 'Demo Merchant' \
//	    --webhook-url   http://localhost:9999/webhook
package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "seed: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		pgURL        = flag.String("pg-url", envOr("SELATPAY_DB_URL", "postgres://selatpay:selatpay@localhost:5432/selatpay?sslmode=disable"), "Postgres connection string")
		pepper       = flag.String("pepper", envOr("SELATPAY_API_KEY_PEPPER", ""), "API key pepper (must match selatpayd config)")
		merchantName = flag.String("merchant-name", "Demo Merchant", "Display name for the seeded merchant")
		webhookURL   = flag.String("webhook-url", "http://localhost:9999/webhook", "Webhook URL the dispatcher should POST to")
		keyID        = flag.String("key-id", "demo-key-001", "Stable identifier for the demo API key")
	)
	flag.Parse()
	if *pepper == "" {
		return errors.New("--pepper is required (or SELATPAY_API_KEY_PEPPER env)")
	}

	rawSecret, secretHex, err := generateSecretHex()
	if err != nil {
		return fmt.Errorf("rand secret: %w", err)
	}
	derived := deriveSecret([]byte(*pepper), rawSecret)
	derivedHex := hex.EncodeToString(derived)

	webhookSecret, _, err := generateSecretHex()
	if err != nil {
		return fmt.Errorf("rand webhook secret: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, *pgURL)
	if err != nil {
		return fmt.Errorf("pgx connect: %w", err)
	}
	defer pool.Close()

	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var merchantID uuid.UUID
	if err := tx.QueryRow(ctx, `
		INSERT INTO merchants (name, webhook_url, webhook_secret)
		VALUES ($1, $2, $3)
		RETURNING id
	`, *merchantName, *webhookURL, webhookSecret).Scan(&merchantID); err != nil {
		return fmt.Errorf("insert merchant: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO api_keys (merchant_id, key_id, secret_hash)
		VALUES ($1, $2, $3)
	`, merchantID, *keyID, derived); err != nil {
		return fmt.Errorf("insert api_key: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	out := struct {
		MerchantID    string `json:"merchant_id"`
		MerchantName  string `json:"merchant_name"`
		KeyID         string `json:"key_id"`
		RawSecretHex  string `json:"raw_secret_hex"`
		SignSecretHex string `json:"sign_secret_hex"`
		WebhookURL    string `json:"webhook_url"`
		WebhookSecret string `json:"webhook_secret_hex"`
	}{
		MerchantID:    merchantID.String(),
		MerchantName:  *merchantName,
		KeyID:         *keyID,
		RawSecretHex:  secretHex,
		SignSecretHex: derivedHex,
		WebhookURL:    *webhookURL,
		WebhookSecret: hex.EncodeToString(webhookSecret),
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func generateSecretHex() ([]byte, string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return nil, "", err
	}
	return buf, hex.EncodeToString(buf), nil
}

func deriveSecret(pepper, raw []byte) []byte {
	mac := hmac.New(sha256.New, pepper)
	mac.Write(raw)
	return mac.Sum(nil)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
