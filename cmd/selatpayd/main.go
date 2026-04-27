// Command selatpayd is the single binary for Selatpay's modular monolith.
// Subcommands map to deployable processes: api, watcher, orchestrator, dispatcher, recon.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/vincentiuslienardo/selatpay/internal/config"
	"github.com/vincentiuslienardo/selatpay/internal/obs"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		os.Exit(1)
	}

	logger := obs.NewLogger(cfg.LogLevel, cfg.Env)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	shutdown, err := obs.SetupTracing(ctx, cfg.ServiceName+"-"+cmd, cfg.OTLPEndpoint, cfg.Env)
	if err != nil {
		logger.Error("otel setup failed", "err", err)
		os.Exit(1)
	}
	defer func() {
		_ = shutdown(context.Background())
	}()

	logger.Info("selatpayd starting", "subcommand", cmd, "env", cfg.Env)

	if err := run(ctx, cmd, args, cfg, logger); err != nil {
		if errors.Is(err, context.Canceled) {
			logger.Info("selatpayd shutdown")
			return
		}
		logger.Error("selatpayd exited with error", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, cmd string, args []string, cfg config.Config, logger *slog.Logger) error {
	_ = args // reserved for per-subcommand flags in subsequent phases
	switch cmd {
	case "api":
		return runAPI(ctx, cfg, logger)
	case "watcher":
		return runWatcher(ctx, cfg, logger)
	case "orchestrator":
		return runOrchestrator(ctx, cfg, logger)
	case "dispatcher":
		return runDispatcher(ctx, cfg, logger)
	case "recon":
		return runStub(ctx, "recon", logger)
	case "version":
		fmt.Println("selatpayd dev")
		return nil
	default:
		usage()
		return fmt.Errorf("unknown subcommand %q", cmd)
	}
}

// runStub keeps the binary buildable while subcommands are implemented in later phases.
func runStub(ctx context.Context, name string, logger *slog.Logger) error {
	logger.Info("subcommand stub running; awaiting implementation", "subcommand", name)
	<-ctx.Done()
	return ctx.Err()
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: selatpayd <subcommand>

subcommands:
  api           HTTP API gateway (REST + HMAC auth)
  watcher       Solana Pay reference watcher
  orchestrator  saga state machine
  dispatcher    webhook outbox dispatcher
  recon         on-chain vs ledger reconciliation
  version       print build info`)
}
