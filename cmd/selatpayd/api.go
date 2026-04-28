package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/gagliardetto/solana-go"
	"github.com/redis/go-redis/v9"

	"github.com/vincentiuslienardo/selatpay/internal/api"
	"github.com/vincentiuslienardo/selatpay/internal/auth"
	"github.com/vincentiuslienardo/selatpay/internal/config"
	ldb "github.com/vincentiuslienardo/selatpay/internal/db"
	"github.com/vincentiuslienardo/selatpay/internal/quoter"
	"github.com/vincentiuslienardo/selatpay/internal/solanapay"
	"github.com/vincentiuslienardo/selatpay/internal/wallet"
)

// usdcDecimals is the hardcoded decimal count for USDC on Solana (same on
// devnet and mainnet). If Selatpay ever denominates in another SPL token
// this has to become mint-configurable.
const usdcDecimals uint8 = 6

func runAPI(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	pool, err := ldb.OpenPool(ctx, cfg.DBURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	var rdb *redis.Client
	if cfg.RedisAddr != "" {
		rdb = redis.NewClient(&redis.Options{Addr: cfg.RedisAddr})
		defer func() { _ = rdb.Close() }()
	}

	hotWalletPub, err := resolveHotWallet(cfg, logger)
	if err != nil {
		return err
	}
	usdcMint, err := solana.PublicKeyFromBase58(cfg.USDCMint)
	if err != nil {
		return fmt.Errorf("parse USDC mint: %w", err)
	}
	allocator, err := solanapay.NewAllocator(cfg.ReferenceEncKey)
	if err != nil {
		return fmt.Errorf("build reference allocator: %w", err)
	}

	q := quoter.New(pool, quoter.NewMockProvider(quoter.DefaultMockRates()), cfg.QuoteHMACSecret, quoter.Options{
		TTL:       cfg.QuoteTTL,
		SpreadBps: cfg.QuoteSpreadBps,
	})

	handler := api.NewRouter(api.Deps{
		Pool:             pool,
		Redis:            rdb,
		Quoter:           q,
		KeyStore:         auth.NewPGKeyStore(pool, cfg.APIKeyPepper),
		IdempotencyTTL:   24 * time.Hour,
		Allocator:        allocator,
		HotWalletPubkey:  hotWalletPub,
		USDCMint:         usdcMint,
		USDCDecimals:     usdcDecimals,
		SolanaPayLabel:   cfg.SolanaPayLabel,
		SolanaPayMessage: cfg.SolanaPayMessage,
	})

	srv := &http.Server{
		Addr:         cfg.HTTPAddr,
		Handler:      handler,
		ReadTimeout:  cfg.HTTPReadTimeout,
		WriteTimeout: cfg.HTTPWriteTimeout,
		BaseContext:  func(_ net.Listener) context.Context { return ctx },
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("http listening", "addr", cfg.HTTPAddr, "hot_wallet", hotWalletPub.String())
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return err
		}
		return <-errCh
	case err := <-errCh:
		return err
	}
}

// resolveHotWallet picks the hot wallet public key from the config. Dev and
// integration deployments populate SELATPAY_HOT_WALLET_SECRET_BASE58 so a
// LocalSigner can be built and its PublicKey taken. Production KMS
// deployments set only SELATPAY_HOT_WALLET_PUBKEY because the private key
// lives inside the HSM. Exactly one must be set at runtime.
func resolveHotWallet(cfg config.Config, logger *slog.Logger) (solana.PublicKey, error) {
	if cfg.HotWalletSecretBase58 != "" {
		signer, err := wallet.NewLocalSignerFromBase58(cfg.HotWalletSecretBase58)
		if err != nil {
			return solana.PublicKey{}, fmt.Errorf("load local signer: %w", err)
		}
		logger.Info("using local hot wallet signer")
		return signer.PublicKey(), nil
	}
	if cfg.HotWalletPubkey != "" {
		pub, err := solana.PublicKeyFromBase58(cfg.HotWalletPubkey)
		if err != nil {
			return solana.PublicKey{}, fmt.Errorf("parse SELATPAY_HOT_WALLET_PUBKEY: %w", err)
		}
		logger.Info("using KMS-stub hot wallet (public key only)")
		return pub, nil
	}
	return solana.PublicKey{}, errors.New(
		"set SELATPAY_HOT_WALLET_SECRET_BASE58 (local signer) " +
			"or SELATPAY_HOT_WALLET_PUBKEY (KMS deployment)")
}
