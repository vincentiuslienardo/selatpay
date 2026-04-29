//go:build integration

package recon_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/rpc"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpg "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/vincentiuslienardo/selatpay/internal/recon"
)

type fakeFetcher struct{ amount string }

func (f *fakeFetcher) GetTokenAccountBalance(ctx context.Context, account solana.PublicKey, commitment rpc.CommitmentType) (*rpc.GetTokenAccountBalanceResult, error) {
	return &rpc.GetTokenAccountBalanceResult{
		Value: &rpc.UiTokenAmount{Amount: f.amount},
	}, nil
}

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
	return pool, func() { pool.Close(); _ = c.Terminate(ctx) }
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

// postDeposit injects a credit_deposit-shaped journal entry: debit
// asset_hot_wallet_usdc, credit liability_user_funds_usdc. The recon
// walker only inspects asset_hot_wallet_usdc; the offsetting credit
// keeps the balanced-entry trigger happy.
func postDeposit(t *testing.T, pool *pgxpool.Pool, externalRef string, amount int64) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var entryID, hot, lib string
	if err := tx.QueryRow(ctx,
		`INSERT INTO journal_entries (external_ref, kind) VALUES ($1, 'deposit_credit') RETURNING id`, externalRef).
		Scan(&entryID); err != nil {
		t.Fatalf("entry: %v", err)
	}
	if err := tx.QueryRow(ctx,
		`SELECT id FROM accounts WHERE code = 'asset_hot_wallet_usdc' AND currency = 'USDC'`).
		Scan(&hot); err != nil {
		t.Fatalf("hot lookup: %v", err)
	}
	if err := tx.QueryRow(ctx,
		`SELECT id FROM accounts WHERE code = 'liability_user_funds_usdc' AND currency = 'USDC'`).
		Scan(&lib); err != nil {
		t.Fatalf("lib lookup: %v", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO postings (journal_entry_id, account_id, amount, currency, direction)
		 VALUES ($1, $2, $3, 'USDC', 'debit'), ($1, $4, $3, 'USDC', 'credit')`,
		entryID, hot, amount, lib); err != nil {
		t.Fatalf("posting: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

func TestWalker_OkWhenChainMatchesLedger(t *testing.T) {
	pool, cleanup := startPostgres(t)
	defer cleanup()

	postDeposit(t, pool, "ref-1", 1_000_000)
	postDeposit(t, pool, "ref-2", 2_500_000)

	w, err := recon.NewWalker(pool, &fakeFetcher{amount: "3500000"},
		solana.NewWallet().PublicKey(), solana.NewWallet().PublicKey(),
		slog.New(slog.NewTextHandler(os.Stderr, nil)),
	)
	if err != nil {
		t.Fatalf("new walker: %v", err)
	}
	report, err := w.Walk(context.Background())
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if report.Status != "ok" {
		t.Errorf("status: got %s want ok (chain=%d ledger=%d)", report.Status, report.OnChainAmount, report.LedgerAmount)
	}
	if report.Difference != 0 {
		t.Errorf("difference: got %d want 0", report.Difference)
	}
}

func TestWalker_DivergedWhenChainExceedsLedger(t *testing.T) {
	pool, cleanup := startPostgres(t)
	defer cleanup()

	postDeposit(t, pool, "ref-1", 1_000_000)

	w, err := recon.NewWalker(pool, &fakeFetcher{amount: "1500000"},
		solana.NewWallet().PublicKey(), solana.NewWallet().PublicKey(), nil,
	)
	if err != nil {
		t.Fatalf("new walker: %v", err)
	}
	report, err := w.Walk(context.Background())
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if report.Status != "diverged" {
		t.Errorf("status: got %s want diverged", report.Status)
	}
	if report.Difference != 500_000 {
		t.Errorf("difference: got %d want 500000", report.Difference)
	}
}

func TestWalker_DivergedWhenLedgerExceedsChain(t *testing.T) {
	pool, cleanup := startPostgres(t)
	defer cleanup()

	postDeposit(t, pool, "ref-1", 5_000_000)

	w, err := recon.NewWalker(pool, &fakeFetcher{amount: "1000000"},
		solana.NewWallet().PublicKey(), solana.NewWallet().PublicKey(), nil,
	)
	if err != nil {
		t.Fatalf("new walker: %v", err)
	}
	report, err := w.Walk(context.Background())
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if report.Status != "diverged" {
		t.Errorf("status: got %s want diverged", report.Status)
	}
	if report.Difference != -4_000_000 {
		t.Errorf("difference: got %d want -4000000", report.Difference)
	}
}
