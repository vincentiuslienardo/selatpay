//go:build integration

package chain

import (
	"context"
	"encoding/base64"
	"encoding/json"
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
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpg "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	ldb "github.com/vincentiuslienardo/selatpay/internal/db"
	dbq "github.com/vincentiuslienardo/selatpay/internal/db/sqlc"
	"github.com/vincentiuslienardo/selatpay/internal/saga"
	"github.com/vincentiuslienardo/selatpay/internal/saga/steps"
)

// --- postgres harness (duplicated from ledger_integration_test.go to avoid
// coupling unrelated test packages through a shared helper module) ---

func startPostgres(t *testing.T) (*pgxpool.Pool, func()) {
	t.Helper()
	ctx := context.Background()

	container, err := tcpg.Run(ctx,
		"postgres:16-alpine",
		tcpg.WithDatabase("selatpay_test"),
		tcpg.WithUsername("test"),
		tcpg.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(45*time.Second),
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

// --- fake RPC client ---

type fakeRPC struct {
	tx      map[solana.Signature]*rpc.GetTransactionResult
	sigs    map[solana.PublicKey][]*rpc.TransactionSignature
	txErr   map[solana.Signature]error
	sigsErr map[solana.PublicKey]error
}

func (f *fakeRPC) GetTransaction(ctx context.Context, sig solana.Signature, _ *rpc.GetTransactionOpts) (*rpc.GetTransactionResult, error) {
	if err, ok := f.txErr[sig]; ok {
		return nil, err
	}
	if r, ok := f.tx[sig]; ok {
		return r, nil
	}
	return nil, rpc.ErrNotFound
}

func (f *fakeRPC) GetSignaturesForAddressWithOpts(ctx context.Context, acct solana.PublicKey, _ *rpc.GetSignaturesForAddressOpts) ([]*rpc.TransactionSignature, error) {
	if err, ok := f.sigsErr[acct]; ok {
		return nil, err
	}
	return f.sigs[acct], nil
}

// buildTxResult synthesises a GetTransactionResult carrying a TransferChecked
// instruction. The transaction is serialized, base64-encoded, and fed back
// through TransactionResultEnvelope.UnmarshalJSON — the public entry point
// solana-go uses when the RPC returns base64-encoded transactions — so the
// envelope's internals are populated the same way a real getTransaction
// response would populate them.
func buildTxResult(t *testing.T, destOverride *solana.PublicKey, mintOverride *solana.PublicKey, amount uint64, decimals uint8, slot uint64, refs ...solana.PublicKey) (*rpc.GetTransactionResult, solana.Signature) {
	t.Helper()
	tx, _, mint, dest, _ := buildTransferCheckedTx(t, amount, decimals, refs...)

	if destOverride != nil {
		// Find & replace the dest key in AccountKeys. buildTransferCheckedTx
		// places it at index 2.
		tx.Message.AccountKeys[2] = *destOverride
		dest = *destOverride
	}
	if mintOverride != nil {
		tx.Message.AccountKeys[3] = *mintOverride
		mint = *mintOverride
	}
	_ = mint
	_ = dest

	raw, err := tx.MarshalBinary()
	if err != nil {
		t.Fatalf("marshal tx: %v", err)
	}
	b64 := base64.StdEncoding.EncodeToString(raw)
	body, err := json.Marshal([]string{b64, string(solana.EncodingBase64)})
	if err != nil {
		t.Fatalf("marshal envelope body: %v", err)
	}
	var env rpc.TransactionResultEnvelope
	if err := env.UnmarshalJSON(body); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}

	bt := solana.UnixTimeSeconds(time.Now().Unix())
	res := &rpc.GetTransactionResult{
		Slot:        slot,
		BlockTime:   &bt,
		Transaction: &env,
	}
	sig := solana.Signature{}
	copy(sig[:], raw[:solana.SignatureLength]) // deterministic-ish stand-in; actual value is opaque for our tests
	// Use a derived signature that's unique per call to avoid collisions when
	// tests build multiple txs. The first 64 bytes of a freshly-serialized
	// transaction include a compact-array header + zeroed signatures, which
	// would collide, so we inject randomness via a wallet.
	copy(sig[:], solana.NewWallet().PublicKey().Bytes())
	return res, sig
}

// --- seed helpers ---

func seedMerchant(t *testing.T, pool *pgxpool.Pool, name string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := pool.QueryRow(context.Background(),
		`INSERT INTO merchants (name) VALUES ($1) RETURNING id`, name).Scan(&id)
	if err != nil {
		t.Fatalf("seed merchant: %v", err)
	}
	return id
}

func seedQuote(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := pool.QueryRow(context.Background(),
		`INSERT INTO quotes (pair, rate_num, rate_scale, spread_bps, expires_at, signature)
		 VALUES ('IDR/USDC', 15000, 0, 50, NOW() + INTERVAL '5 minutes', $1) RETURNING id`,
		[]byte{0xAA}).Scan(&id)
	if err != nil {
		t.Fatalf("seed quote: %v", err)
	}
	return id
}

func seedIntent(t *testing.T, pool *pgxpool.Pool, merchantID, quoteID uuid.UUID, externalRef string, refPub *solana.PublicKey, recipientATA string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	var refStr *string
	if refPub != nil {
		s := refPub.String()
		refStr = &s
	}
	err := pool.QueryRow(context.Background(),
		`INSERT INTO payment_intents (merchant_id, external_ref, amount_idr, quoted_amount_usdc, quote_id, reference_pubkey, recipient_ata)
		 VALUES ($1, $2, 15000, 1000000, $3, $4, $5) RETURNING id`,
		merchantID, externalRef, quoteID, refStr, recipientATA).Scan(&id)
	if err != nil {
		t.Fatalf("seed intent: %v", err)
	}
	return id
}

// --- tests ---

func TestProcessSignature_HappyPath(t *testing.T) {
	pool, cleanup := startPostgres(t)
	defer cleanup()

	hotATA := solana.NewWallet().PublicKey()
	mint := solana.NewWallet().PublicKey()
	ref := solana.NewWallet().PublicKey()

	merchID := seedMerchant(t, pool, "acme")
	quoteID := seedQuote(t, pool)
	intentID := seedIntent(t, pool, merchID, quoteID, "order-1", &ref, hotATA.String())

	res, sig := buildTxResult(t, &hotATA, &mint, 1_000_000, 6, 42, ref)
	fake := &fakeRPC{tx: map[solana.Signature]*rpc.GetTransactionResult{sig: res}}

	w := NewWatcher(pool, fake, nil, WatcherConfig{
		HotWalletATA:     hotATA,
		Mint:             mint,
		ExpectedDecimals: 6,
	}, slog.New(slog.NewTextHandler(os.Stderr, nil)))

	if err := w.RefreshReferences(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if err := w.ProcessSignature(context.Background(), sig, rpc.CommitmentConfirmed); err != nil {
		t.Fatalf("process: %v", err)
	}

	q := dbq.New(pool)
	got, err := q.GetOnchainPaymentBySignature(context.Background(), sig.String())
	if err != nil {
		t.Fatalf("get onchain payment: %v", err)
	}
	if got.Amount != 1_000_000 {
		t.Errorf("amount: got %d want 1000000", got.Amount)
	}
	if got.Commitment != dbq.SolanaCommitmentConfirmed {
		t.Errorf("commitment: got %s want confirmed", got.Commitment)
	}
	if got.ToAta != hotATA.String() {
		t.Errorf("to_ata: got %s want %s", got.ToAta, hotATA)
	}
	if got.Mint != mint.String() {
		t.Errorf("mint: got %s want %s", got.Mint, mint)
	}
	if got.ReferencePubkey == nil || *got.ReferencePubkey != ref.String() {
		t.Errorf("reference_pubkey: got %v want %s", got.ReferencePubkey, ref)
	}
	if gotIntent := ldb.FromPgUUIDPtr(got.IntentID); gotIntent == nil || *gotIntent != intentID {
		t.Errorf("intent_id: got %v want %s", gotIntent, intentID)
	}
}

func TestProcessSignature_SkipsDifferentATA(t *testing.T) {
	pool, cleanup := startPostgres(t)
	defer cleanup()

	hotATA := solana.NewWallet().PublicKey()
	mint := solana.NewWallet().PublicKey()
	otherATA := solana.NewWallet().PublicKey()

	res, sig := buildTxResult(t, &otherATA, &mint, 500, 6, 10)
	fake := &fakeRPC{tx: map[solana.Signature]*rpc.GetTransactionResult{sig: res}}

	w := NewWatcher(pool, fake, nil, WatcherConfig{
		HotWalletATA:     hotATA,
		Mint:             mint,
		ExpectedDecimals: 6,
	}, slog.Default())

	if err := w.ProcessSignature(context.Background(), sig, rpc.CommitmentConfirmed); err != nil {
		t.Fatalf("process: %v", err)
	}
	var count int
	if err := pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM onchain_payments`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 rows, got %d", count)
	}
}

func TestProcessSignature_SkipsWrongMint(t *testing.T) {
	pool, cleanup := startPostgres(t)
	defer cleanup()

	hotATA := solana.NewWallet().PublicKey()
	configuredMint := solana.NewWallet().PublicKey()
	someOtherMint := solana.NewWallet().PublicKey()

	res, sig := buildTxResult(t, &hotATA, &someOtherMint, 500, 6, 10)
	fake := &fakeRPC{tx: map[solana.Signature]*rpc.GetTransactionResult{sig: res}}

	w := NewWatcher(pool, fake, nil, WatcherConfig{
		HotWalletATA:     hotATA,
		Mint:             configuredMint,
		ExpectedDecimals: 6,
	}, slog.Default())

	if err := w.ProcessSignature(context.Background(), sig, rpc.CommitmentConfirmed); err != nil {
		t.Fatalf("process: %v", err)
	}
	var count int
	_ = pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM onchain_payments`).Scan(&count)
	if count != 0 {
		t.Errorf("expected 0 rows, got %d", count)
	}
}

func TestProcessSignature_UnknownReferenceRecordsButUnlinked(t *testing.T) {
	pool, cleanup := startPostgres(t)
	defer cleanup()

	hotATA := solana.NewWallet().PublicKey()
	mint := solana.NewWallet().PublicKey()
	randomRef := solana.NewWallet().PublicKey()

	res, sig := buildTxResult(t, &hotATA, &mint, 777, 6, 11, randomRef)
	fake := &fakeRPC{tx: map[solana.Signature]*rpc.GetTransactionResult{sig: res}}

	w := NewWatcher(pool, fake, nil, WatcherConfig{
		HotWalletATA:     hotATA,
		Mint:             mint,
		ExpectedDecimals: 6,
	}, slog.Default())

	if err := w.ProcessSignature(context.Background(), sig, rpc.CommitmentConfirmed); err != nil {
		t.Fatalf("process: %v", err)
	}
	q := dbq.New(pool)
	row, err := q.GetOnchainPaymentBySignature(context.Background(), sig.String())
	if err != nil {
		t.Fatalf("expected a row: %v", err)
	}
	if row.IntentID.Valid {
		t.Errorf("intent_id should be NULL for unknown reference, got %s", ldb.FromPgUUIDPtr(row.IntentID))
	}
}

func TestProcessSignature_CommitmentMonotonic(t *testing.T) {
	pool, cleanup := startPostgres(t)
	defer cleanup()

	hotATA := solana.NewWallet().PublicKey()
	mint := solana.NewWallet().PublicKey()

	res, sig := buildTxResult(t, &hotATA, &mint, 123, 6, 12)
	fake := &fakeRPC{tx: map[solana.Signature]*rpc.GetTransactionResult{sig: res}}

	w := NewWatcher(pool, fake, nil, WatcherConfig{
		HotWalletATA:     hotATA,
		Mint:             mint,
		ExpectedDecimals: 6,
	}, slog.Default())

	ctx := context.Background()

	if err := w.ProcessSignature(ctx, sig, rpc.CommitmentFinalized); err != nil {
		t.Fatalf("finalized first: %v", err)
	}
	// A late confirmed replay must not downgrade the row.
	if err := w.ProcessSignature(ctx, sig, rpc.CommitmentConfirmed); err != nil {
		t.Fatalf("confirmed replay: %v", err)
	}
	q := dbq.New(pool)
	row, err := q.GetOnchainPaymentBySignature(ctx, sig.String())
	if err != nil {
		t.Fatalf("get row: %v", err)
	}
	if row.Commitment != dbq.SolanaCommitmentFinalized {
		t.Errorf("commitment downgraded: got %s want finalized", row.Commitment)
	}
}

func TestPromoteUnfinalized_PromotesConfirmedToFinalized(t *testing.T) {
	pool, cleanup := startPostgres(t)
	defer cleanup()

	hotATA := solana.NewWallet().PublicKey()
	mint := solana.NewWallet().PublicKey()

	res, sig := buildTxResult(t, &hotATA, &mint, 999, 6, 20)
	fake := &fakeRPC{tx: map[solana.Signature]*rpc.GetTransactionResult{sig: res}}

	w := NewWatcher(pool, fake, nil, WatcherConfig{
		HotWalletATA:     hotATA,
		Mint:             mint,
		ExpectedDecimals: 6,
	}, slog.Default())

	ctx := context.Background()
	if err := w.ProcessSignature(ctx, sig, rpc.CommitmentConfirmed); err != nil {
		t.Fatalf("initial confirmed: %v", err)
	}
	if err := w.PromoteUnfinalized(ctx); err != nil {
		t.Fatalf("promote: %v", err)
	}

	q := dbq.New(pool)
	row, err := q.GetOnchainPaymentBySignature(ctx, sig.String())
	if err != nil {
		t.Fatalf("get row: %v", err)
	}
	if row.Commitment != dbq.SolanaCommitmentFinalized {
		t.Errorf("commitment: got %s want finalized", row.Commitment)
	}
}

func TestProcessSignature_FinalizedLinkedDepositEnqueuesSaga(t *testing.T) {
	pool, cleanup := startPostgres(t)
	defer cleanup()

	hotATA := solana.NewWallet().PublicKey()
	mint := solana.NewWallet().PublicKey()
	ref := solana.NewWallet().PublicKey()

	merchID := seedMerchant(t, pool, "saga-enqueue")
	quoteID := seedQuote(t, pool)
	intentID := seedIntent(t, pool, merchID, quoteID, "saga-enqueue-1", &ref, hotATA.String())

	res, sig := buildTxResult(t, &hotATA, &mint, 750_000, 6, 99, ref)
	fake := &fakeRPC{tx: map[solana.Signature]*rpc.GetTransactionResult{sig: res}}

	w := NewWatcher(pool, fake, nil, WatcherConfig{
		HotWalletATA:     hotATA,
		Mint:             mint,
		ExpectedDecimals: 6,
	}, slog.Default())
	ctx := context.Background()

	if err := w.RefreshReferences(ctx); err != nil {
		t.Fatalf("refresh references: %v", err)
	}

	// A confirmed-only finalize should NOT trigger a saga; only finalized does.
	if err := w.ProcessSignature(ctx, sig, rpc.CommitmentConfirmed); err != nil {
		t.Fatalf("confirmed process: %v", err)
	}
	q := dbq.New(pool)
	if _, err := q.GetSagaRunByIntent(ctx, dbq.GetSagaRunByIntentParams{
		IntentID: ldb.PgUUID(intentID),
		SagaKind: string(saga.KindSettlement),
	}); err == nil {
		t.Fatal("saga should not exist after confirmed-only commitment")
	}

	if err := w.ProcessSignature(ctx, sig, rpc.CommitmentFinalized); err != nil {
		t.Fatalf("finalized process: %v", err)
	}
	run, err := q.GetSagaRunByIntent(ctx, dbq.GetSagaRunByIntentParams{
		IntentID: ldb.PgUUID(intentID),
		SagaKind: string(saga.KindSettlement),
	})
	if err != nil {
		t.Fatalf("expected saga after finalize: %v", err)
	}
	if run.CurrentStep != steps.FirstStep {
		t.Errorf("current_step: got %s want %s", run.CurrentStep, steps.FirstStep)
	}
	if run.State != dbq.SagaStatePending {
		t.Errorf("saga state: got %s want pending", run.State)
	}

	// A second finalized event must not enqueue a duplicate saga.
	if err := w.ProcessSignature(ctx, sig, rpc.CommitmentFinalized); err != nil {
		t.Fatalf("finalized replay: %v", err)
	}
	var sagaCount int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM saga_runs WHERE intent_id = $1`, intentID).Scan(&sagaCount); err != nil {
		t.Fatalf("count saga runs: %v", err)
	}
	if sagaCount != 1 {
		t.Errorf("saga_runs after replay: got %d want 1", sagaCount)
	}
}

func TestPollReferences_FetchesAndPersists(t *testing.T) {
	pool, cleanup := startPostgres(t)
	defer cleanup()

	hotATA := solana.NewWallet().PublicKey()
	mint := solana.NewWallet().PublicKey()
	ref := solana.NewWallet().PublicKey()

	merchID := seedMerchant(t, pool, "polling-test")
	quoteID := seedQuote(t, pool)
	seedIntent(t, pool, merchID, quoteID, "poll-order", &ref, hotATA.String())

	res, sig := buildTxResult(t, &hotATA, &mint, 555, 6, 33, ref)
	fake := &fakeRPC{
		tx: map[solana.Signature]*rpc.GetTransactionResult{sig: res},
		sigs: map[solana.PublicKey][]*rpc.TransactionSignature{
			ref: {{Signature: sig}},
		},
	}

	w := NewWatcher(pool, fake, nil, WatcherConfig{
		HotWalletATA:     hotATA,
		Mint:             mint,
		ExpectedDecimals: 6,
	}, slog.Default())

	ctx := context.Background()
	if err := w.RefreshReferences(ctx); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if err := w.PollReferences(ctx); err != nil {
		t.Fatalf("poll: %v", err)
	}

	q := dbq.New(pool)
	row, err := q.GetOnchainPaymentBySignature(ctx, sig.String())
	if err != nil {
		t.Fatalf("row: %v", err)
	}
	if row.Amount != 555 {
		t.Errorf("amount: got %d want 555", row.Amount)
	}
}
