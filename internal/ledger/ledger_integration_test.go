//go:build integration

package ledger_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpg "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/vincentiuslienardo/selatpay/internal/ledger"
)

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

// stripGooseDown keeps only the "Up" portion of a goose migration file and
// drops the goose statement markers. Good enough for tests — not a goose
// reimplementation.
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

func TestPost_BalancedEntryPersists(t *testing.T) {
	pool, cleanup := startPostgres(t)
	defer cleanup()
	l := ledger.New(pool)
	ctx := context.Background()

	cash, err := l.CreateAccount(ctx, "cash", ledger.AccountAsset, "USD")
	if err != nil {
		t.Fatalf("create cash: %v", err)
	}
	payable, err := l.CreateAccount(ctx, "payable", ledger.AccountLiability, "USD")
	if err != nil {
		t.Fatalf("create payable: %v", err)
	}

	entry, err := l.Post(ctx, ledger.Entry{
		ExternalRef: "ref-1",
		Kind:        "deposit_credit",
		Lines: []ledger.Line{
			{AccountID: cash.ID, Amount: 10_000, Currency: "USD", Direction: ledger.Debit},
			{AccountID: payable.ID, Amount: 10_000, Currency: "USD", Direction: ledger.Credit},
		},
	})
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if entry.ID == uuid.Nil {
		t.Fatal("expected entry id")
	}

	cashBal, _ := l.BalanceOf(ctx, cash.ID)
	payBal, _ := l.BalanceOf(ctx, payable.ID)
	if cashBal.Amount != 10_000 {
		t.Fatalf("cash balance: got %d, want 10000", cashBal.Amount)
	}
	if payBal.Amount != 10_000 {
		t.Fatalf("payable balance: got %d, want 10000 (credit-normal)", payBal.Amount)
	}
}

func TestPost_UnbalancedEntryRejectedByDB(t *testing.T) {
	pool, cleanup := startPostgres(t)
	defer cleanup()
	l := ledger.New(pool)
	ctx := context.Background()

	a1, _ := l.CreateAccount(ctx, "a1", ledger.AccountAsset, "USD")
	a2, _ := l.CreateAccount(ctx, "a2", ledger.AccountLiability, "USD")

	// Bypass in-process Validate by poking the DB directly with an unbalanced
	// entry. The DEFERRED constraint trigger must still fire at commit.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var entryID uuid.UUID
	if err := tx.QueryRow(ctx,
		`INSERT INTO journal_entries (external_ref, kind) VALUES ($1, $2) RETURNING id`,
		"ref-bad", "test").Scan(&entryID); err != nil {
		t.Fatalf("insert entry: %v", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO postings (journal_entry_id, account_id, amount, currency, direction) VALUES ($1,$2,$3,$4,$5)`,
		entryID, a1.ID, int64(100), "USD", "debit"); err != nil {
		t.Fatalf("insert debit: %v", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO postings (journal_entry_id, account_id, amount, currency, direction) VALUES ($1,$2,$3,$4,$5)`,
		entryID, a2.ID, int64(50), "USD", "credit"); err != nil {
		t.Fatalf("insert credit: %v", err)
	}

	if err := tx.Commit(ctx); err == nil {
		t.Fatalf("expected commit to fail due to balanced-entry trigger")
	}
}

func TestPost_CurrencyMismatchRejectedByDB(t *testing.T) {
	pool, cleanup := startPostgres(t)
	defer cleanup()
	l := ledger.New(pool)
	ctx := context.Background()

	usdAcct, _ := l.CreateAccount(ctx, "usd-only", ledger.AccountAsset, "USD")
	idrAcct, _ := l.CreateAccount(ctx, "idr-only", ledger.AccountLiability, "IDR")

	_, err := l.Post(ctx, ledger.Entry{
		ExternalRef: "ref-mixed",
		Kind:        "test",
		Lines: []ledger.Line{
			{AccountID: usdAcct.ID, Amount: 100, Currency: "IDR", Direction: ledger.Debit},
			{AccountID: idrAcct.ID, Amount: 100, Currency: "IDR", Direction: ledger.Credit},
		},
	})
	if err == nil {
		t.Fatalf("expected currency-mismatch trigger to reject")
	}
}

func TestPost_IdempotentByExternalRef(t *testing.T) {
	pool, cleanup := startPostgres(t)
	defer cleanup()
	l := ledger.New(pool)
	ctx := context.Background()

	a, _ := l.CreateAccount(ctx, "a", ledger.AccountAsset, "USD")
	b, _ := l.CreateAccount(ctx, "b", ledger.AccountLiability, "USD")

	e := ledger.Entry{
		ExternalRef: "idem-1",
		Kind:        "test",
		Lines: []ledger.Line{
			{AccountID: a.ID, Amount: 500, Currency: "USD", Direction: ledger.Debit},
			{AccountID: b.ID, Amount: 500, Currency: "USD", Direction: ledger.Credit},
		},
	}

	first, err := l.Post(ctx, e)
	if err != nil {
		t.Fatalf("first post: %v", err)
	}
	second, err := l.Post(ctx, e)
	if err != nil {
		t.Fatalf("second post: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("idempotent post must return same entry id")
	}

	// Balance must not be doubled.
	bal, _ := l.BalanceOf(ctx, a.ID)
	if bal.Amount != 500 {
		t.Fatalf("idempotent post must not duplicate postings, balance=%d", bal.Amount)
	}
}
