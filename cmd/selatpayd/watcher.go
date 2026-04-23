package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/rpc/ws"

	"github.com/vincentiuslienardo/selatpay/internal/chain"
	"github.com/vincentiuslienardo/selatpay/internal/config"
	ldb "github.com/vincentiuslienardo/selatpay/internal/db"
)

// watcherDecimals pins the expected mint decimals to 6 because Selatpay
// denominates in USDC today. If a second stable ever joins (USDT-SPL is
// also 6 on mainnet), this stays a constant; the watcher uses it to
// reject a mint whose on-chain decimals disagree with what we configured.
const watcherDecimals uint8 = 6

func runWatcher(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
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
	dialWS := func(c context.Context) (*ws.Client, error) {
		return chainClient.DialWS(c)
	}

	watcher := chain.NewWatcher(pool, chainClient.RPC, dialWS, chain.WatcherConfig{
		HotWalletATA:     hotWalletATA,
		Mint:             usdcMint,
		ExpectedDecimals: watcherDecimals,
	}, logger)

	logger.Info("watcher starting",
		"hot_wallet", hotWalletPub.String(),
		"hot_wallet_ata", hotWalletATA.String(),
		"mint", usdcMint.String(),
		"rpc", cfg.SolanaRPCURL,
		"ws", cfg.SolanaWSURL,
	)
	return watcher.Run(ctx)
}
