package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/vincentiuslienardo/selatpay/internal/api"
	"github.com/vincentiuslienardo/selatpay/internal/auth"
	"github.com/vincentiuslienardo/selatpay/internal/config"
	ldb "github.com/vincentiuslienardo/selatpay/internal/db"
	"github.com/vincentiuslienardo/selatpay/internal/quoter"
)

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

	q := quoter.New(pool, quoter.NewMockProvider(quoter.DefaultMockRates()), cfg.QuoteHMACSecret, quoter.Options{
		TTL:       cfg.QuoteTTL,
		SpreadBps: int32(cfg.QuoteSpreadBps),
	})

	handler := api.NewRouter(api.Deps{
		Pool:           pool,
		Redis:          rdb,
		Quoter:         q,
		KeyStore:       auth.NewPGKeyStore(pool, cfg.APIKeyPepper),
		IdempotencyTTL: 24 * time.Hour,
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
		logger.Info("http listening", "addr", cfg.HTTPAddr)
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
