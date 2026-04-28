//go:build integration

package outbox_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpg "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/vincentiuslienardo/selatpay/internal/outbox"
)

// --- shared scaffolding ---

func startPostgres(t *testing.T) (*pgxpool.Pool, func()) {
	t.Helper()
	ctx := context.Background()
	container, err := tcpg.Run(ctx,
		"postgres:16-alpine",
		tcpg.WithDatabase("selatpay_test"),
		tcpg.WithUsername("test"),
		tcpg.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(30*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("conn string: %v", err)
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
		_ = container.Terminate(ctx)
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
		sql := stripGooseDown(string(raw))
		if _, err := pool.Exec(ctx, sql); err != nil {
			return fmt.Errorf("apply %s: %w", f.Name(), err)
		}
	}
	return nil
}

func stripGooseDown(s string) string {
	if i := strings.Index(s, "-- +goose Down"); i >= 0 {
		s = s[:i]
	}
	s = strings.ReplaceAll(s, "-- +goose Up", "")
	s = strings.ReplaceAll(s, "-- +goose StatementBegin", "")
	s = strings.ReplaceAll(s, "-- +goose StatementEnd", "")
	return s
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

// publishViaTx wraps Publish in its own transaction so tests don't have
// to manage transactions to seed outbox rows.
func publishViaTx(t *testing.T, pool *pgxpool.Pool, topic string, payload []byte) outbox.Message {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	id := uuid.New()
	msg, err := outbox.Publish(ctx, tx, topic, &id, payload, map[string]string{"X-Test": "1"})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return msg
}

// --- tests ---

func TestDispatcher_DeliversAndMarksRows(t *testing.T) {
	pool, cleanup := startPostgres(t)
	defer cleanup()

	publishViaTx(t, pool, "intent.completed", []byte(`{"a":1}`))
	publishViaTx(t, pool, "intent.completed", []byte(`{"a":2}`))

	var delivered atomic.Int64
	sender := outbox.SenderFunc(func(ctx context.Context, msg outbox.Message) error {
		delivered.Add(1)
		return nil
	})

	d, err := outbox.NewDispatcher(pool, sender, outbox.DispatcherConfig{
		Topic:        "intent.completed",
		PollInterval: 20 * time.Millisecond,
		IdleBackoff:  30 * time.Millisecond,
		BatchSize:    8,
		MaxAttempts:  3,
	}, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		t.Fatalf("dispatcher: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()

	deadline := time.Now().Add(4 * time.Second)
	for {
		if delivered.Load() >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for deliveries; got %d", delivered.Load())
		}
		time.Sleep(50 * time.Millisecond)
	}

	cancel()
	<-done

	var undeliveredCount int
	if err := pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM outbox WHERE topic = 'intent.completed' AND delivered_at IS NULL`).Scan(&undeliveredCount); err != nil {
		t.Fatalf("count undelivered: %v", err)
	}
	if undeliveredCount != 0 {
		t.Errorf("undelivered remaining: %d", undeliveredCount)
	}
}

func TestDispatcher_FailedSendsAreScheduledForRetry(t *testing.T) {
	pool, cleanup := startPostgres(t)
	defer cleanup()

	publishViaTx(t, pool, "intent.completed", []byte(`{}`))

	sender := outbox.SenderFunc(func(ctx context.Context, msg outbox.Message) error {
		return errors.New("downstream 500")
	})

	d, err := outbox.NewDispatcher(pool, sender, outbox.DispatcherConfig{
		Topic:        "intent.completed",
		PollInterval: 20 * time.Millisecond,
		IdleBackoff:  20 * time.Millisecond,
		BatchSize:    8,
		MaxAttempts:  10,
		BackoffBase:  250 * time.Millisecond,
		BackoffMax:   500 * time.Millisecond,
	}, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		t.Fatalf("dispatcher: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()

	deadline := time.Now().Add(1500 * time.Millisecond)
	var attempts int32
	for time.Now().Before(deadline) {
		if err := pool.QueryRow(context.Background(),
			`SELECT attempts FROM outbox WHERE topic = 'intent.completed' LIMIT 1`).Scan(&attempts); err != nil {
			t.Fatalf("read attempts: %v", err)
		}
		if attempts >= 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	<-done

	if attempts < 1 {
		t.Fatalf("expected at least one retry attempt recorded, got %d", attempts)
	}

	var deliveredCount int
	if err := pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM outbox WHERE delivered_at IS NOT NULL`).Scan(&deliveredCount); err != nil {
		t.Fatalf("count delivered: %v", err)
	}
	if deliveredCount != 0 {
		t.Errorf("expected 0 delivered, got %d", deliveredCount)
	}
}

func TestDispatcher_AdvisoryLockServializesProcesses(t *testing.T) {
	pool, cleanup := startPostgres(t)
	defer cleanup()

	publishViaTx(t, pool, "intent.completed", []byte(`{}`))

	build := func(owner string, sender outbox.Sender) *outbox.Dispatcher {
		d, err := outbox.NewDispatcher(pool, sender, outbox.DispatcherConfig{
			Topic:          "intent.completed",
			PollInterval:   20 * time.Millisecond,
			IdleBackoff:    30 * time.Millisecond,
			BatchSize:      4,
			MaxAttempts:    2,
			LockRetryEvery: 100 * time.Millisecond,
		}, slog.New(slog.NewTextHandler(os.Stderr, nil)).With("dispatcher", owner))
		if err != nil {
			t.Fatalf("new dispatcher %s: %v", owner, err)
		}
		return d
	}

	var receivedA, receivedB atomic.Int32
	senderA := outbox.SenderFunc(func(ctx context.Context, msg outbox.Message) error {
		receivedA.Add(1)
		return nil
	})
	senderB := outbox.SenderFunc(func(ctx context.Context, msg outbox.Message) error {
		receivedB.Add(1)
		return nil
	})

	dA := build("A", senderA)
	dB := build("B", senderB)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = dA.Run(ctx) }()
	go func() { defer wg.Done(); _ = dB.Run(ctx) }()

	deadline := time.Now().Add(3 * time.Second)
	for {
		if receivedA.Load()+receivedB.Load() >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no delivery observed within deadline")
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	wg.Wait()

	totalSeen := receivedA.Load() + receivedB.Load()
	if totalSeen != 1 {
		t.Fatalf("expected exactly one dispatcher to deliver, A=%d B=%d", receivedA.Load(), receivedB.Load())
	}
	if receivedA.Load() > 0 && receivedB.Load() > 0 {
		t.Fatalf("both dispatchers delivered (advisory lock failed): A=%d B=%d",
			receivedA.Load(), receivedB.Load())
	}
}
