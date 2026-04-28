//go:build integration

package saga_test

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

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpg "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	ldb "github.com/vincentiuslienardo/selatpay/internal/db"
	dbq "github.com/vincentiuslienardo/selatpay/internal/db/sqlc"
	"github.com/vincentiuslienardo/selatpay/internal/ledger"
	"github.com/vincentiuslienardo/selatpay/internal/saga"
	"github.com/vincentiuslienardo/selatpay/internal/saga/steps"
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

// --- fixtures ---

type sagaFixture struct {
	pool       *pgxpool.Pool
	merchantID uuid.UUID
	quoteID    uuid.UUID
	intentID   uuid.UUID
	depositSig string
	depositAmt int64
}

func seedFixture(t *testing.T, pool *pgxpool.Pool) sagaFixture {
	t.Helper()
	ctx := context.Background()

	var merchantID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO merchants (name) VALUES ('saga-merchant') RETURNING id`).Scan(&merchantID); err != nil {
		t.Fatalf("seed merchant: %v", err)
	}

	var quoteID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO quotes (pair, rate_num, rate_scale, spread_bps, expires_at, signature)
		 VALUES ('IDR/USDC', 15000, 0, 50, NOW() + INTERVAL '5 minutes', $1) RETURNING id`,
		[]byte{0xAA}).Scan(&quoteID); err != nil {
		t.Fatalf("seed quote: %v", err)
	}

	var intentID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO payment_intents (merchant_id, external_ref, amount_idr, quoted_amount_usdc, quote_id, recipient_ata, state)
		 VALUES ($1, 'saga-1', 15000000, 1000000, $2, 'recipient-ata', 'pending') RETURNING id`,
		merchantID, quoteID).Scan(&intentID); err != nil {
		t.Fatalf("seed intent: %v", err)
	}

	depositSig := "saga-deposit-sig"
	depositAmt := int64(1_000_000)
	if _, err := pool.Exec(ctx,
		`INSERT INTO onchain_payments
		 (signature, slot, from_ata, to_ata, mint, amount, commitment, intent_id)
		 VALUES ($1, 100, 'payer-ata', 'recipient-ata', 'mint', $2, 'finalized', $3)`,
		depositSig, depositAmt, intentID); err != nil {
		t.Fatalf("seed onchain payment: %v", err)
	}

	return sagaFixture{
		pool:       pool,
		merchantID: merchantID,
		quoteID:    quoteID,
		intentID:   intentID,
		depositSig: depositSig,
		depositAmt: depositAmt,
	}
}

func enqueueSaga(t *testing.T, pool *pgxpool.Pool, intentID uuid.UUID) {
	t.Helper()
	q := dbq.New(pool)
	if _, err := q.EnqueueSagaRun(context.Background(), dbq.EnqueueSagaRunParams{
		IntentID:    ldb.PgUUID(intentID),
		SagaKind:    string(saga.KindSettlement),
		CurrentStep: steps.FirstStep,
	}); err != nil {
		t.Fatalf("enqueue saga: %v", err)
	}
}

func newRunner(t *testing.T, pool *pgxpool.Pool) *saga.Runner {
	t.Helper()
	deps := steps.Deps{Ledger: ledger.New(pool), Log: slog.New(slog.NewTextHandler(os.Stderr, nil))}
	registry := saga.NewRegistry(steps.All(deps)...)
	r, err := saga.NewRunner(pool, registry, saga.Config{
		Owner:        "test-runner",
		PollInterval: 10 * time.Millisecond,
		IdleBackoff:  20 * time.Millisecond,
		MaxAttempts:  3,
	}, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		t.Fatalf("new runner: %v", err)
	}
	return r
}

// --- tests ---

func TestSagaRunner_HappyPathWalksToCompleted(t *testing.T) {
	pool, cleanup := startPostgres(t)
	defer cleanup()
	fx := seedFixture(t, pool)
	enqueueSaga(t, pool, fx.intentID)

	runner := newRunner(t, pool)
	ctx := context.Background()

	// Four steps: credit_deposit → trigger_payout → apply_payout_result → emit_completed.
	// Each Tick advances exactly one. A fifth Tick should find nothing to do.
	for i := 0; i < 4; i++ {
		didWork, err := runner.Tick(ctx)
		if err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
		if !didWork {
			t.Fatalf("tick %d: expected work, found none", i)
		}
	}

	// Saga completed.
	q := dbq.New(pool)
	run, err := q.GetSagaRunByIntent(ctx, dbq.GetSagaRunByIntentParams{
		IntentID: ldb.PgUUID(fx.intentID),
		SagaKind: string(saga.KindSettlement),
	})
	if err != nil {
		t.Fatalf("get saga: %v", err)
	}
	if run.State != dbq.SagaStateCompleted {
		t.Errorf("saga state: got %s want completed", run.State)
	}

	// Intent advanced to completed.
	intent, err := q.GetPaymentIntentByID(ctx, ldb.PgUUID(fx.intentID))
	if err != nil {
		t.Fatalf("get intent: %v", err)
	}
	if intent.State != dbq.PaymentIntentStateCompleted {
		t.Errorf("intent state: got %s want completed", intent.State)
	}

	// One deposit-credit journal entry, balanced and amount-correct.
	var jeCount int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM journal_entries WHERE intent_id = $1 AND kind = 'deposit_credit'`, fx.intentID).Scan(&jeCount); err != nil {
		t.Fatalf("count journal entries: %v", err)
	}
	if jeCount != 1 {
		t.Errorf("deposit_credit journal entry count: got %d want 1", jeCount)
	}

	var postingCount int
	var postingSum int64
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*), COALESCE(SUM(CASE direction WHEN 'debit' THEN amount ELSE -amount END), 0)
		 FROM postings p JOIN journal_entries j ON p.journal_entry_id = j.id
		 WHERE j.intent_id = $1`, fx.intentID).Scan(&postingCount, &postingSum); err != nil {
		t.Fatalf("postings sum: %v", err)
	}
	if postingCount != 2 {
		t.Errorf("posting count: got %d want 2", postingCount)
	}
	if postingSum != 0 {
		t.Errorf("postings unbalanced: %d (debit-credit)", postingSum)
	}

	// Outbox row exists for intent.completed.
	var outboxCount int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM outbox WHERE topic = 'intent.completed' AND aggregate_id = $1`, fx.intentID).Scan(&outboxCount); err != nil {
		t.Fatalf("count outbox: %v", err)
	}
	if outboxCount != 1 {
		t.Errorf("outbox count: got %d want 1", outboxCount)
	}

	// Idle: nothing to claim.
	didWork, err := runner.Tick(ctx)
	if err != nil {
		t.Fatalf("post-completion tick: %v", err)
	}
	if didWork {
		t.Errorf("expected idle after completion")
	}
}

func TestSagaRunner_CreditDepositReplayIsIdempotent(t *testing.T) {
	pool, cleanup := startPostgres(t)
	defer cleanup()
	fx := seedFixture(t, pool)
	enqueueSaga(t, pool, fx.intentID)

	runner := newRunner(t, pool)
	ctx := context.Background()

	// First credit_deposit lands.
	didWork, err := runner.Tick(ctx)
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	if !didWork {
		t.Fatal("expected first tick to work")
	}

	// Force the saga back to credit_deposit so the next tick re-runs the
	// same step with the same external_ref. This is the moral equivalent
	// of a worker dying after a tx commit but before logging — the
	// runner reclaims the saga and re-executes; idempotency on
	// external_ref is what keeps the ledger from double-posting.
	if _, err := pool.Exec(ctx,
		`UPDATE saga_runs SET current_step = 'credit_deposit', state = 'pending', next_run_at = NOW(),
		 lease_owner = NULL, lease_until = NULL WHERE intent_id = $1`, fx.intentID); err != nil {
		t.Fatalf("rewind saga: %v", err)
	}

	didWork, err = runner.Tick(ctx)
	if err != nil {
		t.Fatalf("replay tick: %v", err)
	}
	if !didWork {
		t.Fatal("expected replay tick to claim saga")
	}

	// Still exactly one journal entry.
	var jeCount int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM journal_entries WHERE intent_id = $1 AND kind = 'deposit_credit'`, fx.intentID).Scan(&jeCount); err != nil {
		t.Fatalf("count journal entries: %v", err)
	}
	if jeCount != 1 {
		t.Errorf("deposit_credit journal entry count after replay: got %d want 1", jeCount)
	}
}

func TestSagaRunner_UnknownStepFailsTerminally(t *testing.T) {
	pool, cleanup := startPostgres(t)
	defer cleanup()
	fx := seedFixture(t, pool)

	q := dbq.New(pool)
	if _, err := q.EnqueueSagaRun(context.Background(), dbq.EnqueueSagaRunParams{
		IntentID:    ldb.PgUUID(fx.intentID),
		SagaKind:    string(saga.KindSettlement),
		CurrentStep: "step_that_does_not_exist",
	}); err != nil {
		t.Fatalf("enqueue saga: %v", err)
	}

	runner := newRunner(t, pool)
	if didWork, err := runner.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	} else if !didWork {
		t.Fatal("expected work")
	}

	run, err := q.GetSagaRunByIntent(context.Background(), dbq.GetSagaRunByIntentParams{
		IntentID: ldb.PgUUID(fx.intentID),
		SagaKind: string(saga.KindSettlement),
	})
	if err != nil {
		t.Fatalf("get saga: %v", err)
	}
	if run.State != dbq.SagaStateFailed {
		t.Errorf("saga state: got %s want failed", run.State)
	}
}

func TestSagaRunner_ConcurrentClaimsAreExclusive(t *testing.T) {
	pool, cleanup := startPostgres(t)
	defer cleanup()
	fx := seedFixture(t, pool)
	enqueueSaga(t, pool, fx.intentID)

	deps := steps.Deps{Ledger: ledger.New(pool), Log: slog.Default()}
	registry := saga.NewRegistry(steps.All(deps)...)
	r1, _ := saga.NewRunner(pool, registry, saga.Config{Owner: "r1"}, slog.Default())
	r2, _ := saga.NewRunner(pool, registry, saga.Config{Owner: "r2"}, slog.Default())

	// r1 and r2 race; SKIP LOCKED guarantees only one observes the row.
	type ack struct {
		owner   string
		didWork bool
		err     error
	}
	results := make(chan ack, 2)
	go func() {
		didWork, err := r1.Tick(context.Background())
		results <- ack{owner: "r1", didWork: didWork, err: err}
	}()
	go func() {
		didWork, err := r2.Tick(context.Background())
		results <- ack{owner: "r2", didWork: didWork, err: err}
	}()

	gotWork := 0
	for i := 0; i < 2; i++ {
		a := <-results
		if a.err != nil {
			t.Fatalf("%s: %v", a.owner, a.err)
		}
		if a.didWork {
			gotWork++
		}
	}
	// At least one and at most two — but two would mean both ran the
	// same step concurrently, which violates exclusivity. SKIP LOCKED
	// guarantees exactly one.
	if gotWork != 1 {
		t.Fatalf("expected exactly one runner to claim, got %d", gotWork)
	}
}
