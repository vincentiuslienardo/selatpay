package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"

	"github.com/gagliardetto/solana-go"

	"github.com/vincentiuslienardo/selatpay/internal/chain"
	"github.com/vincentiuslienardo/selatpay/internal/config"
	ldb "github.com/vincentiuslienardo/selatpay/internal/db"
	"github.com/vincentiuslienardo/selatpay/internal/recon"
)

// runRecon executes a single reconciliation pass and exits with code
// 1 if the on-chain balance and ledger diverge. Cron or k8s
// CronJob is responsible for scheduling; the binary itself does not
// loop because schedulers are easier to operate when the workload is
// a discrete one-shot than a daemon with internal timing.
func runRecon(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	pool, err := ldb.OpenPool(ctx, cfg.DBURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	hotWalletPub, err := resolveHotWallet(cfg, logger)
	if err != nil {
		return err
	}
	usdcMint, err := solana.PublicKeyFromBase58(cfg.USDCMint)
	if err != nil {
		return fmt.Errorf("parse USDC mint: %w", err)
	}
	hotWalletATA, _, err := solana.FindAssociatedTokenAddress(hotWalletPub, usdcMint)
	if err != nil {
		return fmt.Errorf("derive hot wallet ATA: %w", err)
	}

	chainClient := chain.NewClient(cfg.SolanaRPCURL, cfg.SolanaWSURL)
	walker, err := recon.NewWalker(pool, chainClient.RPC, hotWalletATA, usdcMint, logger)
	if err != nil {
		return err
	}

	report, err := walker.Walk(ctx)
	if err != nil {
		return err
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(map[string]any{
		"status":           report.Status,
		"hot_wallet_ata":   report.HotWalletATA.String(),
		"mint":             report.Mint.String(),
		"on_chain_amount":  report.OnChainAmount,
		"ledger_amount":    report.LedgerAmount,
		"difference":       report.Difference,
		"generated_at":     report.GeneratedAt.UTC().Format("2006-01-02T15:04:05.000Z"),
	}); err != nil {
		return fmt.Errorf("encode report: %w", err)
	}

	if report.Status != "ok" {
		// Non-zero exit signals divergence to whatever scheduler
		// invokes the subcommand; ops dashboards (and the cron
		// failure mailbox) treat this as the loud-fail signal.
		logger.Error("recon diverged",
			"on_chain", report.OnChainAmount,
			"ledger", report.LedgerAmount,
			"difference", report.Difference,
		)
		os.Exit(1)
	}
	logger.Info("recon ok",
		"on_chain", report.OnChainAmount,
		"ledger", report.LedgerAmount,
	)
	return nil
}
