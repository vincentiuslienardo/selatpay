package chain

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/rpc"
	"github.com/gagliardetto/solana-go/rpc/ws"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	ldb "github.com/vincentiuslienardo/selatpay/internal/db"
	dbq "github.com/vincentiuslienardo/selatpay/internal/db/sqlc"
	"github.com/vincentiuslienardo/selatpay/internal/saga"
	"github.com/vincentiuslienardo/selatpay/internal/saga/steps"
)

// RPCClient is the narrow RPC surface the watcher uses. Stating it as an
// interface lets unit tests wire a stub without standing up a live RPC
// endpoint, and keeps the watcher blind to which RPC provider is in play
// (direct devnet, Helius, a self-hosted node).
type RPCClient interface {
	GetTransaction(ctx context.Context, sig solana.Signature, opts *rpc.GetTransactionOpts) (*rpc.GetTransactionResult, error)
	GetSignaturesForAddressWithOpts(ctx context.Context, acct solana.PublicKey, opts *rpc.GetSignaturesForAddressOpts) ([]*rpc.TransactionSignature, error)
}

// WatcherConfig bundles the tunables that shape the watcher's cadence and
// validation contract. Zero values for intervals fall back to production
// defaults inside NewWatcher so the api subcommand can still build a
// half-initialized watcher struct for config sanity checks.
type WatcherConfig struct {
	HotWalletATA     solana.PublicKey
	Mint             solana.PublicKey
	ExpectedDecimals uint8

	RefreshInterval time.Duration
	PollInterval    time.Duration
	PromoteInterval time.Duration

	PollLimit        int
	PromoteBatchSize int32
}

// Watcher persists every SPL deposit that lands on the hot wallet ATA,
// matches it against known Solana Pay reference pubkeys to bind an intent,
// and promotes each row's commitment from confirmed to finalized as the
// cluster catches up. It is the single writer for onchain_payments.
type Watcher struct {
	pool   *pgxpool.Pool
	rpc    RPCClient
	dialWS func(ctx context.Context) (*ws.Client, error)
	cfg    WatcherConfig
	log    *slog.Logger

	mu   sync.RWMutex
	refs map[solana.PublicKey]uuid.UUID
}

// NewWatcher wires the dependencies and applies defaults for unset cadence
// fields. dialWS may be nil in tests that exercise ProcessSignature without
// the subscribe loop; Run still starts the other loops regardless.
func NewWatcher(pool *pgxpool.Pool, rpcClient RPCClient, dialWS func(context.Context) (*ws.Client, error), cfg WatcherConfig, log *slog.Logger) *Watcher {
	if cfg.RefreshInterval <= 0 {
		cfg.RefreshInterval = 10 * time.Second
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 15 * time.Second
	}
	if cfg.PromoteInterval <= 0 {
		cfg.PromoteInterval = 10 * time.Second
	}
	if cfg.PollLimit <= 0 {
		cfg.PollLimit = 20
	}
	if cfg.PromoteBatchSize <= 0 {
		cfg.PromoteBatchSize = 50
	}
	if log == nil {
		log = slog.Default()
	}
	return &Watcher{
		pool:   pool,
		rpc:    rpcClient,
		dialWS: dialWS,
		cfg:    cfg,
		log:    log,
		refs:   make(map[solana.PublicKey]uuid.UUID),
	}
}

// Run starts every background loop and blocks until ctx is cancelled. Any
// loop that crashes is logged and restarted by its own retry policy; Run
// returns once ctx fires so the subcommand harness can proceed to
// graceful shutdown.
func (w *Watcher) Run(ctx context.Context) error {
	if err := w.RefreshReferences(ctx); err != nil {
		w.log.Warn("initial reference refresh failed", "err", err)
	}

	loops := []struct {
		name string
		fn   func(context.Context) error
	}{
		{"refresh", w.refreshLoop},
		{"poll", w.pollLoop},
		{"promote", w.promoteLoop},
		{"subscribe", w.subscribeLoop},
	}
	var wg sync.WaitGroup
	for _, l := range loops {
		wg.Add(1)
		go func(name string, fn func(context.Context) error) {
			defer wg.Done()
			if err := fn(ctx); err != nil && !errors.Is(err, context.Canceled) {
				w.log.Error("watcher loop exited", "loop", name, "err", err)
			}
		}(l.name, l.fn)
	}
	wg.Wait()
	return ctx.Err()
}

// RefreshReferences reloads the in-memory reference → intent_id map from
// active (pending|funded) payment intents. The watcher only needs to know
// which references are still live; completed and expired intents no
// longer match incoming deposits, so dropping them keeps the match set
// bounded and cheap to consult.
func (w *Watcher) RefreshReferences(ctx context.Context) error {
	q := dbq.New(w.pool)
	rows, err := q.ListActiveReferenceIntents(ctx)
	if err != nil {
		return fmt.Errorf("chain: list active references: %w", err)
	}
	next := make(map[solana.PublicKey]uuid.UUID, len(rows))
	for _, r := range rows {
		if r.ReferencePubkey == nil {
			continue
		}
		pub, err := solana.PublicKeyFromBase58(*r.ReferencePubkey)
		if err != nil {
			w.log.Warn("skipping invalid reference pubkey", "intent", ldb.FromPgUUID(r.ID), "pubkey", *r.ReferencePubkey, "err", err)
			continue
		}
		next[pub] = ldb.FromPgUUID(r.ID)
	}
	w.mu.Lock()
	w.refs = next
	w.mu.Unlock()
	return nil
}

// ProcessSignature fetches the transaction at the requested commitment,
// parses its SPL transfer, matches against the hot wallet ATA and known
// references, and upserts onchain_payments. It is idempotent and
// commitment-monotonic by construction: the upsert's CASE clause only
// advances commitment forward, so replaying a confirmed event after its
// finalized event never downgrades the row.
func (w *Watcher) ProcessSignature(ctx context.Context, sig solana.Signature, commitment rpc.CommitmentType) error {
	resp, err := w.rpc.GetTransaction(ctx, sig, &rpc.GetTransactionOpts{
		Encoding:   solana.EncodingBase64,
		Commitment: commitment,
	})
	if err != nil {
		if errors.Is(err, rpc.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("chain: getTransaction %s: %w", sig, err)
	}
	if resp == nil || resp.Transaction == nil {
		return nil
	}
	if resp.Meta != nil && resp.Meta.Err != nil {
		return nil
	}
	tx, err := resp.Transaction.GetTransaction()
	if err != nil {
		return fmt.Errorf("chain: decode transaction %s: %w", sig, err)
	}
	transfer, err := ParseSPLTransferChecked(tx)
	if err != nil {
		if errors.Is(err, ErrNoSPLTransfer) {
			return nil
		}
		return fmt.Errorf("chain: parse transfer %s: %w", sig, err)
	}
	if !transfer.Dest.Equals(w.cfg.HotWalletATA) {
		return nil
	}
	if !transfer.Mint.Equals(w.cfg.Mint) {
		return nil
	}
	if transfer.Decimals != w.cfg.ExpectedDecimals {
		return fmt.Errorf("chain: mint decimals mismatch: tx=%d want=%d", transfer.Decimals, w.cfg.ExpectedDecimals)
	}
	if transfer.Amount > uint64(1)<<63-1 {
		return fmt.Errorf("chain: amount %d exceeds int64 range", transfer.Amount)
	}

	intentID, refPub := w.matchReference(tx)

	blockTime := pgtype.Timestamptz{}
	if resp.BlockTime != nil {
		blockTime = pgtype.Timestamptz{Time: resp.BlockTime.Time(), Valid: true}
	}

	var refPtr *string
	if refPub != nil {
		s := refPub.String()
		refPtr = &s
	}

	dbtx, err := w.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("chain: begin tx: %w", err)
	}
	defer func() { _ = dbtx.Rollback(ctx) }()

	q := dbq.New(dbtx)
	row, err := q.UpsertOnchainPayment(ctx, dbq.UpsertOnchainPaymentParams{
		Signature: sig.String(),
		// Solana slot numbers are bounded by the cluster's lifetime and
		// fit in int64 by orders of magnitude. The schema column is
		// BIGINT to match.
		Slot:            int64(resp.Slot), //nolint:gosec // see comment above
		BlockTime:       blockTime,
		FromAta:         transfer.Source.String(),
		ToAta:           transfer.Dest.String(),
		Mint:            transfer.Mint.String(),
		Amount:          int64(transfer.Amount),
		ReferencePubkey: refPtr,
		Commitment:      commitmentToDB(commitment),
		IntentID:        ldb.PgUUIDPtr(intentIDPtr(intentID)),
	})
	if err != nil {
		return fmt.Errorf("chain: upsert onchain_payment %s: %w", sig, err)
	}

	// The watcher is the saga's only producer: a finalized, intent-linked
	// deposit is the trigger that hands settlement off to the orchestrator.
	// Doing the enqueue inside the same tx as the upsert is what gives
	// Phase 5 its atomicity guarantee — there is no observable state where
	// onchain_payments shows finalized but no saga exists to drive it.
	// EnqueueSagaRun is idempotent on (intent_id, saga_kind), so a replay
	// (e.g., promote loop revisiting an already-finalized row) is a no-op.
	if row.Commitment == dbq.SolanaCommitmentFinalized && row.IntentID.Valid {
		if _, err := q.EnqueueSagaRun(ctx, dbq.EnqueueSagaRunParams{
			IntentID:    row.IntentID,
			SagaKind:    string(saga.KindSettlement),
			CurrentStep: steps.FirstStep,
		}); err != nil {
			return fmt.Errorf("chain: enqueue saga run for %s: %w", sig, err)
		}
	}

	if err := dbtx.Commit(ctx); err != nil {
		return fmt.Errorf("chain: commit tx: %w", err)
	}
	w.log.Info("onchain payment upserted",
		"sig", row.Signature,
		"amount", row.Amount,
		"commitment", row.Commitment,
		"intent", ldb.FromPgUUIDPtr(row.IntentID),
	)
	return nil
}

// PromoteUnfinalized re-examines every onchain_payments row that has not
// yet reached finalized commitment and re-runs ProcessSignature against
// rpc.CommitmentFinalized. The upsert's CASE clause ensures commitments
// only advance forward; this loop's only job is to give the row a chance
// to promote once the cluster has finalized the slot.
func (w *Watcher) PromoteUnfinalized(ctx context.Context) error {
	q := dbq.New(w.pool)
	rows, err := q.ListUnfinalizedOnchainPayments(ctx, w.cfg.PromoteBatchSize)
	if err != nil {
		return fmt.Errorf("chain: list unfinalized: %w", err)
	}
	for _, r := range rows {
		sig, err := solana.SignatureFromBase58(r.Signature)
		if err != nil {
			w.log.Warn("bad signature in unfinalized row", "sig", r.Signature, "err", err)
			continue
		}
		if perr := w.ProcessSignature(ctx, sig, rpc.CommitmentFinalized); perr != nil {
			w.log.Warn("promote signature", "sig", sig, "err", perr)
		}
	}
	return nil
}

// PollReferences sweeps every active reference for recent signatures at
// confirmed commitment. It is the resilience net under subscribeLoop: if
// the WS stream drops a message, or if we were offline when the payer
// sent, the poller will still catch the signature on its next tick.
func (w *Watcher) PollReferences(ctx context.Context) error {
	w.mu.RLock()
	refs := make([]solana.PublicKey, 0, len(w.refs))
	for k := range w.refs {
		refs = append(refs, k)
	}
	w.mu.RUnlock()

	limit := w.cfg.PollLimit
	for _, ref := range refs {
		sigs, err := w.rpc.GetSignaturesForAddressWithOpts(ctx, ref, &rpc.GetSignaturesForAddressOpts{
			Limit:      &limit,
			Commitment: rpc.CommitmentConfirmed,
		})
		if err != nil {
			w.log.Warn("poll signatures", "ref", ref, "err", err)
			continue
		}
		for _, s := range sigs {
			if s.Err != nil {
				continue
			}
			if perr := w.ProcessSignature(ctx, s.Signature, rpc.CommitmentConfirmed); perr != nil {
				w.log.Warn("poll process", "sig", s.Signature, "err", perr)
			}
		}
	}
	return nil
}

func (w *Watcher) refreshLoop(ctx context.Context) error {
	t := time.NewTicker(w.cfg.RefreshInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			if err := w.RefreshReferences(ctx); err != nil {
				w.log.Warn("refresh references", "err", err)
			}
		}
	}
}

func (w *Watcher) pollLoop(ctx context.Context) error {
	t := time.NewTicker(w.cfg.PollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			if err := w.PollReferences(ctx); err != nil {
				w.log.Warn("poll references", "err", err)
			}
		}
	}
}

func (w *Watcher) promoteLoop(ctx context.Context) error {
	t := time.NewTicker(w.cfg.PromoteInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			if err := w.PromoteUnfinalized(ctx); err != nil {
				w.log.Warn("promote unfinalized", "err", err)
			}
		}
	}
}

// subscribeLoop maintains a single logsSubscribe(mentions=hotWalletATA) WS
// connection with exponential backoff on disconnect. Reconnects happen
// with a fresh ws.Client rather than reusing the old one, so a partial
// protocol error cannot poison subsequent subscriptions.
func (w *Watcher) subscribeLoop(ctx context.Context) error {
	if w.dialWS == nil {
		w.log.Info("subscribe loop disabled (no WS dialer)")
		<-ctx.Done()
		return ctx.Err()
	}
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := w.runSubscription(ctx); err != nil {
			if errors.Is(err, context.Canceled) {
				return err
			}
			w.log.Warn("log subscription lost", "err", err, "retry_in", backoff)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		return nil
	}
}

func (w *Watcher) runSubscription(ctx context.Context) error {
	wsClient, err := w.dialWS(ctx)
	if err != nil {
		return err
	}
	defer wsClient.Close()

	sub, err := wsClient.LogsSubscribeMentions(w.cfg.HotWalletATA, rpc.CommitmentConfirmed)
	if err != nil {
		return fmt.Errorf("logsSubscribe: %w", err)
	}
	defer sub.Unsubscribe()
	w.log.Info("log subscription established", "mentions", w.cfg.HotWalletATA.String())

	for {
		msg, err := sub.Recv(ctx)
		if err != nil {
			return err
		}
		if msg.Value.Err != nil {
			continue
		}
		sig := msg.Value.Signature
		if perr := w.ProcessSignature(ctx, sig, rpc.CommitmentConfirmed); perr != nil {
			w.log.Warn("process signature", "sig", sig, "err", perr)
		}
	}
}

func (w *Watcher) matchReference(tx *solana.Transaction) (uuid.UUID, *solana.PublicKey) {
	w.mu.RLock()
	known := make(map[solana.PublicKey]struct{}, len(w.refs))
	for k := range w.refs {
		known[k] = struct{}{}
	}
	w.mu.RUnlock()

	hits := ReferencesIn(tx, known)
	if len(hits) == 0 {
		return uuid.Nil, nil
	}
	// Multiple reference hits shouldn't happen under normal payer flow
	// (one reference per intent); if they do, take the first and log.
	if len(hits) > 1 {
		w.log.Warn("multiple reference matches on tx; using first", "hits", hits)
	}
	pub := hits[0]
	w.mu.RLock()
	id := w.refs[pub]
	w.mu.RUnlock()
	return id, &pub
}

func commitmentToDB(c rpc.CommitmentType) dbq.SolanaCommitment {
	switch c {
	case rpc.CommitmentConfirmed:
		return dbq.SolanaCommitmentConfirmed
	case rpc.CommitmentFinalized:
		return dbq.SolanaCommitmentFinalized
	default:
		return dbq.SolanaCommitmentProcessed
	}
}

func intentIDPtr(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}
