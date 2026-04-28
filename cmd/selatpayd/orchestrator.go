package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/vincentiuslienardo/selatpay/internal/config"
	ldb "github.com/vincentiuslienardo/selatpay/internal/db"
	"github.com/vincentiuslienardo/selatpay/internal/ledger"
	"github.com/vincentiuslienardo/selatpay/internal/payout"
	"github.com/vincentiuslienardo/selatpay/internal/payout/rails"
	"github.com/vincentiuslienardo/selatpay/internal/saga"
	"github.com/vincentiuslienardo/selatpay/internal/saga/steps"
)

func runOrchestrator(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	pool, err := ldb.OpenPool(ctx, cfg.DBURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	router, err := buildPayoutRouter(cfg)
	if err != nil {
		return err
	}

	deps := steps.Deps{
		Ledger:      ledger.New(pool),
		PayoutRails: router,
		Log:         logger,
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

	logger.Info("orchestrator starting", "owner", owner, "payout_rails", router.Names())
	return runner.Run(ctx)
}

// buildPayoutRouter assembles the rails the orchestrator can drive
// at boot time. Right now that's just mock_idr_bank for the IDR
// corridor; production rails (xendit, flip, wise) plug in here as
// new constructors. Validating each rail's config up front means a
// misconfigured deploy crashes the orchestrator on start rather
// than at first payout.
func buildPayoutRouter(cfg config.Config) (*payout.Router, error) {
	if err := rails.Validate(cfg.MockBankURL); err != nil {
		return nil, fmt.Errorf("orchestrator: mock IDR bank config: %w", err)
	}
	mockRail := rails.NewMockIDRBank(cfg.MockBankURL, nil)
	return payout.NewRouter(mockRail), nil
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
