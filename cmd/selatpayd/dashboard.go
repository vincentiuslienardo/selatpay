package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/vincentiuslienardo/selatpay/internal/config"
	"github.com/vincentiuslienardo/selatpay/internal/dashboard"
	ldb "github.com/vincentiuslienardo/selatpay/internal/db"
)

// runDashboard serves the read-only htmx + Go templates view at
// cfg.DashboardAddr. Read-only by construction: no handlers
// mutate state, so an operator dropping in mid-flow cannot
// accidentally change a running saga or post a journal entry from
// the browser.
func runDashboard(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	pool, err := ldb.OpenPool(ctx, cfg.DBURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	srv, err := dashboard.NewServer(pool)
	if err != nil {
		return err
	}

	httpSrv := &http.Server{
		Addr:              cfg.DashboardAddr,
		Handler:           srv,
		ReadHeaderTimeout: 5 * time.Second,
		BaseContext:       func(_ net.Listener) context.Context { return ctx },
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("dashboard listening", "addr", cfg.DashboardAddr)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
		return <-errCh
	case err := <-errCh:
		return err
	}
}
