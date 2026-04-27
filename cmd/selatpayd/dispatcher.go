package main

import (
	"context"
	"log/slog"

	"github.com/vincentiuslienardo/selatpay/internal/config"
	ldb "github.com/vincentiuslienardo/selatpay/internal/db"
	"github.com/vincentiuslienardo/selatpay/internal/outbox"
)

// runDispatcher boots an outbox dispatcher for the intent.completed
// topic. The actual webhook delivery — HMAC signing, retry policy
// against arbitrary merchant URLs — lands in Phase 7. For Phase 5 the
// Sender is a logging stub so the saga's outbox publish path can be
// exercised end-to-end against a running orchestrator.
func runDispatcher(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	pool, err := ldb.OpenPool(ctx, cfg.DBURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	sender := outbox.SenderFunc(func(ctx context.Context, msg outbox.Message) error {
		logger.Info("outbox message ready for delivery (stub)",
			"id", msg.ID,
			"topic", msg.Topic,
			"aggregate_id", msg.AggregateID,
			"attempts", msg.Attempts,
		)
		return nil
	})

	d, err := outbox.NewDispatcher(pool, sender, outbox.DispatcherConfig{
		Topic: "intent.completed",
	}, logger)
	if err != nil {
		return err
	}

	logger.Info("dispatcher starting", "topic", "intent.completed")
	return d.Run(ctx)
}
