//go:build integration

package webhook_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpg "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/vincentiuslienardo/selatpay/internal/outbox"
	"github.com/vincentiuslienardo/selatpay/internal/webhook"
)

func startPostgres(t *testing.T) (*pgxpool.Pool, func()) {
	t.Helper()
	ctx := context.Background()
	c, err := tcpg.Run(ctx, "postgres:16-alpine",
		tcpg.WithDatabase("selatpay_test"),
		tcpg.WithUsername("test"),
		tcpg.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(30*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("postgres: %v", err)
	}
	dsn, err := c.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("dsn: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	if err := applyMigrations(ctx, pool); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	return pool, func() {
		pool.Close()
		_ = c.Terminate(ctx)
	}
}

func applyMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	root, err := repoRoot()
	if err != nil {
		return err
	}
	dir := filepath.Join(root, "internal", "db", "migrations")
	files, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, f := range files {
		if f.IsDir() || !strings.HasSuffix(f.Name(), ".sql") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, f.Name()))
		if err != nil {
			return err
		}
		s := string(raw)
		if i := strings.Index(s, "-- +goose Down"); i >= 0 {
			s = s[:i]
		}
		s = strings.ReplaceAll(s, "-- +goose Up", "")
		s = strings.ReplaceAll(s, "-- +goose StatementBegin", "")
		s = strings.ReplaceAll(s, "-- +goose StatementEnd", "")
		if _, err := pool.Exec(ctx, s); err != nil {
			return fmt.Errorf("apply %s: %w", f.Name(), err)
		}
	}
	return nil
}

func repoRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("go.mod not found")
		}
		dir = parent
	}
}

func seedMerchant(t *testing.T, pool *pgxpool.Pool, url string, secret []byte) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := pool.QueryRow(context.Background(),
		`INSERT INTO merchants (name, webhook_url, webhook_secret) VALUES ('webhook-merchant', $1, $2) RETURNING id`,
		url, secret).Scan(&id)
	if err != nil {
		t.Fatalf("seed merchant: %v", err)
	}
	return id
}

func TestSender_DeliversWithValidSignature(t *testing.T) {
	pool, cleanup := startPostgres(t)
	defer cleanup()

	secret := []byte("merchant-secret")
	body := []byte(`{"event":"intent.completed","intent_id":"abc"}`)

	var seenSig atomic.Value
	var seenBody atomic.Pointer[[]byte]
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenSig.Store(r.Header.Get(webhook.SignatureHeader))
		b, _ := io.ReadAll(r.Body)
		seenBody.Store(&b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	merchantID := seedMerchant(t, pool, srv.URL, secret)

	now := time.Unix(1_700_000_100, 0)
	sender, err := webhook.NewSender(pool, webhook.Config{
		Clock: func() time.Time { return now },
		Log:   slog.New(slog.NewTextHandler(os.Stderr, nil)),
	})
	if err != nil {
		t.Fatalf("new sender: %v", err)
	}

	msg := outbox.Message{
		ID:      uuid.New(),
		Topic:   "intent.completed",
		Payload: body,
		Headers: map[string]string{
			webhook.MerchantHeader: merchantID.String(),
			"X-Selatpay-Topic":     "intent.completed",
		},
	}
	if err := sender.Send(context.Background(), msg); err != nil {
		t.Fatalf("send: %v", err)
	}

	sig, _ := seenSig.Load().(string)
	if sig == "" {
		t.Fatal("receiver never observed the signature header")
	}
	if err := webhook.Verify(secret, *seenBody.Load(), sig, now, time.Minute); err != nil {
		t.Fatalf("receiver-side verify: %v", err)
	}
}

func TestSender_NoOpWhenMerchantHasNoConfig(t *testing.T) {
	pool, cleanup := startPostgres(t)
	defer cleanup()

	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// Seed without webhook config.
	var merchantID uuid.UUID
	err := pool.QueryRow(context.Background(),
		`INSERT INTO merchants (name) VALUES ('no-webhook') RETURNING id`).Scan(&merchantID)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	sender, err := webhook.NewSender(pool, webhook.Config{Log: slog.New(slog.NewTextHandler(os.Stderr, nil))})
	if err != nil {
		t.Fatalf("new sender: %v", err)
	}
	err = sender.Send(context.Background(), outbox.Message{
		ID:      uuid.New(),
		Topic:   "intent.completed",
		Payload: []byte("{}"),
		Headers: map[string]string{webhook.MerchantHeader: merchantID.String()},
	})
	if err != nil {
		t.Fatalf("expected no-op nil, got %v", err)
	}
	if hits.Load() != 0 {
		t.Errorf("receiver should not have been called, hits=%d", hits.Load())
	}
}

func TestSender_NonSuccessStatusReturnsError(t *testing.T) {
	pool, cleanup := startPostgres(t)
	defer cleanup()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	merchantID := seedMerchant(t, pool, srv.URL, []byte("secret"))

	sender, err := webhook.NewSender(pool, webhook.Config{Log: slog.New(slog.NewTextHandler(os.Stderr, nil))})
	if err != nil {
		t.Fatalf("new sender: %v", err)
	}

	err = sender.Send(context.Background(), outbox.Message{
		ID:      uuid.New(),
		Topic:   "intent.completed",
		Payload: []byte("{}"),
		Headers: map[string]string{webhook.MerchantHeader: merchantID.String()},
	})
	if err == nil {
		t.Error("expected error from 500 response")
	}
}
