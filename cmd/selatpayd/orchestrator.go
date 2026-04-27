package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/vincentiuslienardo/selatpay/internal/config"
	ldb "github.com/vincentiuslienardo/selatpay/internal/db"
	"github.com/vincentiuslienardo/selatpay/internal/ledger"
	"github.com/vincentiuslienardo/selatpay/internal/saga"
	"github.com/vincentiuslienardo/selatpay/internal/saga/steps"
)

func runOrchestrator(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	pool, err := ldb.OpenPool(ctx, cfg.DBURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	deps := steps.Deps{
		Ledger: ledger.New(pool),
		Log:    logger,
	}
	registry := saga.NewRegistry(steps.All(deps)...)

	owner, err := orchestratorOwner()
	if err != nil {
		return err
	}
	runner, err := saga.NewRunner(pool, registry, saga.Config{Owner: owner}, logger)
	if err != nil {
		return err
	}

	logger.Info("orchestrator starting", "owner", owner)
	return runner.Run(ctx)
}

// orchestratorOwner stamps each saga claim with a stable identifier the
// operator can grep for in the logs. host:pid is enough for one-binary-
// per-host deployments; multi-replica pods get the pod name through
// HOSTNAME, which is the standard injection in the Kubernetes downward
// API and the default in `docker compose`.
func orchestratorOwner() (string, error) {
	host, err := os.Hostname()
	if err != nil {
		return "", fmt.Errorf("read hostname: %w", err)
	}
	return fmt.Sprintf("%s:%d", host, os.Getpid()), nil
}
