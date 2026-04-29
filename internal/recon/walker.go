// Package recon owns the periodic reconciliation that compares
// on-chain custody (the hot wallet's USDC ATA balance) against
// the ledger's view of the same account. A divergence means we
// either missed a deposit, posted twice, or the chart-of-accounts
// model has drifted from operational reality; either way an
// operator needs to look.
package recon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/rpc"
	"github.com/jackc/pgx/v5/pgxpool"

	dbq "github.com/vincentiuslienardo/selatpay/internal/db/sqlc"
	"github.com/vincentiuslienardo/selatpay/internal/ledger"
)

// TokenBalanceFetcher abstracts the one RPC call the walker needs.
// Stating it as an interface lets tests inject a stub balance
// without standing up a live cluster.
type TokenBalanceFetcher interface {
	GetTokenAccountBalance(ctx context.Context, account solana.PublicKey, commitment rpc.CommitmentType) (*rpc.GetTokenAccountBalanceResult, error)
}

// Report captures one reconciliation pass. Status is "ok" when
// chain and ledger match, "diverged" otherwise. Difference is
// chain minus ledger (positive means chain holds more than the
// books expect — missed deposit; negative means ledger thinks we
// have more — duplicate posting or an off-chain transfer we
// haven't recorded).
type Report struct {
	HotWalletATA   solana.PublicKey
	Mint           solana.PublicKey
	OnChainAmount  int64
	LedgerAmount   int64
	Difference     int64
	Status         string
	GeneratedAt    time.Time
}

// Walker holds the dependencies needed to produce a single Report.
// One Walk(ctx) call performs the full diff and returns it; the
// subcommand wraps that in its scheduling story.
type Walker struct {
	pool         *pgxpool.Pool
	rpc          TokenBalanceFetcher
	hotWalletATA solana.PublicKey
	mint         solana.PublicKey
	commitment   rpc.CommitmentType
	log          *slog.Logger
}

// NewWalker validates inputs at construction so a misconfigured
// recon subcommand fails at boot rather than at first run. Empty
// pubkeys (the zero value) are explicitly rejected.
func NewWalker(pool *pgxpool.Pool, rpcClient TokenBalanceFetcher, hotWalletATA, mint solana.PublicKey, log *slog.Logger) (*Walker, error) {
	if pool == nil {
		return nil, errors.New("recon: pool is required")
	}
	if rpcClient == nil {
		return nil, errors.New("recon: rpc client is required")
	}
	if hotWalletATA.IsZero() {
		return nil, errors.New("recon: hot wallet ATA is required")
	}
	if mint.IsZero() {
		return nil, errors.New("recon: mint is required")
	}
	if log == nil {
		log = slog.Default()
	}
	return &Walker{
		pool:         pool,
		rpc:          rpcClient,
		hotWalletATA: hotWalletATA,
		mint:         mint,
		commitment:   rpc.CommitmentFinalized,
		log:          log,
	}, nil
}

// Walk produces one reconciliation report. It does not write to
// the database; the caller decides whether to persist a history
// row, alert ops, or exit non-zero.
func (w *Walker) Walk(ctx context.Context) (Report, error) {
	onChain, err := w.fetchOnChainBalance(ctx)
	if err != nil {
		return Report{}, fmt.Errorf("recon: fetch on-chain: %w", err)
	}

	q := dbq.New(w.pool)
	row, err := q.AccountBalanceByCode(ctx, dbq.AccountBalanceByCodeParams{
		Code:     ledger.AccountHotWalletUSDC,
		Currency: ledger.CurrencyUSDC,
	})
	if err != nil {
		return Report{}, fmt.Errorf("recon: ledger balance: %w", err)
	}

	report := Report{
		HotWalletATA:  w.hotWalletATA,
		Mint:          w.mint,
		OnChainAmount: onChain,
		LedgerAmount:  row.Balance,
		Difference:    onChain - row.Balance,
		GeneratedAt:   time.Now(),
	}
	if report.Difference == 0 {
		report.Status = "ok"
	} else {
		report.Status = "diverged"
	}
	return report, nil
}

// fetchOnChainBalance pulls the hot wallet ATA balance at the
// configured commitment. The RPC returns Amount as a base-10
// string (Solana SDK convention because token amounts can exceed
// JS number precision); we parse to int64 here so the rest of
// the walker stays in scalar math.
func (w *Walker) fetchOnChainBalance(ctx context.Context) (int64, error) {
	res, err := w.rpc.GetTokenAccountBalance(ctx, w.hotWalletATA, w.commitment)
	if err != nil {
		return 0, err
	}
	if res == nil || res.Value == nil {
		return 0, errors.New("empty response")
	}
	return strconv.ParseInt(res.Value.Amount, 10, 64)
}
