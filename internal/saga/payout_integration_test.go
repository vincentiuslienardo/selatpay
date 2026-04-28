//go:build integration

package saga_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	ldb "github.com/vincentiuslienardo/selatpay/internal/db"
	dbq "github.com/vincentiuslienardo/selatpay/internal/db/sqlc"
	"github.com/vincentiuslienardo/selatpay/internal/ledger"
	"github.com/vincentiuslienardo/selatpay/internal/payout"
	"github.com/vincentiuslienardo/selatpay/internal/payout/rails"
	"github.com/vincentiuslienardo/selatpay/internal/saga"
	"github.com/vincentiuslienardo/selatpay/internal/saga/steps"
)

// runnerWithRail wires a saga.Runner around the real mock_idr_bank
// rail pointed at the supplied httptest server. The rail and
// step bodies execute as they do in production; only the bank URL
// is overridden.
func runnerWithRail(t *testing.T, pool *pgxpool.Pool, baseURL string) *saga.Runner {
	t.Helper()
	rail := rails.NewMockIDRBank(baseURL, &http.Client{Timeout: 2 * time.Second})
	deps := steps.Deps{
		Ledger:      ledger.New(pool),
		PayoutRails: payout.NewRouter(rail),
		Log:         slog.New(slog.NewTextHandler(os.Stderr, nil)),
	}
	registry := saga.NewRegistry(steps.All(deps)...)
	r, err := saga.NewRunner(pool, registry, saga.Config{
		Owner:        "rail-test",
		PollInterval: 5 * time.Millisecond,
		IdleBackoff:  10 * time.Millisecond,
		MaxAttempts:  4,
		BackoffBase:  10 * time.Millisecond,
		BackoffMax:   50 * time.Millisecond,
	}, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		t.Fatalf("new runner: %v", err)
	}
	return r
}

func TestPayoutRail_HappyPathPostsBothJournals(t *testing.T) {
	pool, cleanup := startPostgres(t)
	defer cleanup()
	fx := seedFixture(t, pool)
	enqueueSaga(t, pool, fx.intentID)

	srv := happyPathBank(t)
	defer srv.Close()

	runner := runnerWithRail(t, pool, srv.URL)
	for i := 0; i < 4; i++ {
		didWork, err := runner.Tick(context.Background())
		if err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
		if !didWork {
			t.Fatalf("tick %d: expected work", i)
		}
	}

	q := dbq.New(pool)
	intent, err := q.GetPaymentIntentByID(context.Background(), ldb.PgUUID(fx.intentID))
	if err != nil {
		t.Fatalf("get intent: %v", err)
	}
	if intent.State != dbq.PaymentIntentStateCompleted {
		t.Errorf("intent state: got %s want completed", intent.State)
	}

	po, err := q.GetPayoutByIntent(context.Background(), ldb.PgUUID(fx.intentID))
	if err != nil {
		t.Fatalf("get payout: %v", err)
	}
	if po.State != dbq.PayoutStateSucceeded {
		t.Errorf("payout state: got %s want succeeded", po.State)
	}
	if po.RailReference == nil || *po.RailReference == "" {
		t.Errorf("rail reference not persisted: %v", po.RailReference)
	}

	// Each currency journal must net to zero independently.
	rows, err := pool.Query(context.Background(),
		`SELECT p.currency, SUM(CASE p.direction WHEN 'debit' THEN p.amount ELSE -p.amount END)
		 FROM postings p JOIN journal_entries j ON p.journal_entry_id = j.id
		 WHERE j.intent_id = $1 GROUP BY p.currency`, fx.intentID)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	balances := map[string]int64{}
	for rows.Next() {
		var currency string
		var sum int64
		if err := rows.Scan(&currency, &sum); err != nil {
			t.Fatalf("scan: %v", err)
		}
		balances[currency] = sum
	}
	if balances["USDC"] != 0 {
		t.Errorf("USDC unbalanced: %d", balances["USDC"])
	}
	if balances["IDR"] != 0 {
		t.Errorf("IDR unbalanced: %d", balances["IDR"])
	}

	// Spread shows up on revenue: 1 USDC at mid 15000 = 15000 IDR;
	// merchant got 14925; spread = 75.
	var spread int64
	if err := pool.QueryRow(context.Background(),
		`SELECT COALESCE(SUM(p.amount), 0) FROM postings p
		 JOIN accounts a ON a.id = p.account_id
		 JOIN journal_entries j ON j.id = p.journal_entry_id
		 WHERE j.intent_id = $1 AND a.code = 'revenue_fx_spread_idr' AND p.direction = 'credit'`,
		fx.intentID).Scan(&spread); err != nil {
		t.Fatalf("read spread: %v", err)
	}
	if spread != 75 {
		t.Errorf("spread: got %d want 75", spread)
	}
}

func TestPayoutRail_RetryThenSuccessLandsCleanly(t *testing.T) {
	pool, cleanup := startPostgres(t)
	defer cleanup()
	fx := seedFixture(t, pool)
	enqueueSaga(t, pool, fx.intentID)

	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// First call returns 503; subsequent calls succeed. Same
		// idempotency key on every call demonstrates the dedup
		// contract on the server isn't exercised here (the rail
		// retry is pre-success).
		n := hits.Add(1)
		if n == 1 {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"reference": "MIDR-after-retry",
			"status":    "succeeded",
		})
	}))
	defer srv.Close()

	runner := runnerWithRail(t, pool, srv.URL)
	q := dbq.New(pool)

	// First credit_deposit, then trigger_payout returns retry —
	// runner reschedules with backoff. We tick repeatedly until
	// the saga completes or we run out of patience.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, err := runner.Tick(context.Background())
		if err != nil {
			t.Fatalf("tick: %v", err)
		}
		intent, err := q.GetPaymentIntentByID(context.Background(), ldb.PgUUID(fx.intentID))
		if err != nil {
			t.Fatalf("get intent: %v", err)
		}
		if intent.State == dbq.PaymentIntentStateCompleted {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}

	intent, _ := q.GetPaymentIntentByID(context.Background(), ldb.PgUUID(fx.intentID))
	if intent.State != dbq.PaymentIntentStateCompleted {
		t.Fatalf("intent did not complete: state=%s, hits=%d", intent.State, hits.Load())
	}
	if hits.Load() < 2 {
		t.Errorf("expected at least 2 rail calls (1 retry + 1 success), got %d", hits.Load())
	}
}

func TestPayoutRail_PermanentFailureMarksIntentFailed(t *testing.T) {
	pool, cleanup := startPostgres(t)
	defer cleanup()
	fx := seedFixture(t, pool)
	enqueueSaga(t, pool, fx.intentID)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":  "rejected",
			"message": "account closed",
		})
	}))
	defer srv.Close()

	runner := runnerWithRail(t, pool, srv.URL)
	for i := 0; i < 5; i++ {
		_, err := runner.Tick(context.Background())
		if err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
	}

	q := dbq.New(pool)
	intent, _ := q.GetPaymentIntentByID(context.Background(), ldb.PgUUID(fx.intentID))
	if intent.State != dbq.PaymentIntentStateFailed {
		t.Errorf("intent state: got %s want failed", intent.State)
	}

	po, _ := q.GetPayoutByIntent(context.Background(), ldb.PgUUID(fx.intentID))
	if po.State != dbq.PayoutStateFailed {
		t.Errorf("payout state: got %s want failed", po.State)
	}
	if po.LastError == nil || *po.LastError == "" {
		t.Errorf("expected last_error to carry the rail message")
	}

	// Saga itself terminal-fails.
	run, _ := q.GetSagaRunByIntent(context.Background(), dbq.GetSagaRunByIntentParams{
		IntentID: ldb.PgUUID(fx.intentID),
		SagaKind: string(saga.KindSettlement),
	})
	if run.State != dbq.SagaStateFailed {
		t.Errorf("saga state: got %s want failed", run.State)
	}

	// No payout journal should exist — we stopped before
	// apply_payout_result. Only the deposit_credit journal stands.
	var kinds int
	if err := pool.QueryRow(context.Background(),
		`SELECT COUNT(DISTINCT kind) FROM journal_entries WHERE intent_id = $1`, fx.intentID).Scan(&kinds); err != nil {
		t.Fatalf("count kinds: %v", err)
	}
	if kinds != 1 {
		t.Errorf("expected only deposit_credit journal, got %d kinds", kinds)
	}
}

// happyPathBank is a tiny httptest server that always returns 200
// with a synthetic reference, regardless of body or query string.
func happyPathBank(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Idempotency-Key") == "" {
			http.Error(w, "missing idempotency key", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"reference": "MIDR-happy",
			"status":    "succeeded",
		})
	}))
}
