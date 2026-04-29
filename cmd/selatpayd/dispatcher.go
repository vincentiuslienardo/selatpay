package main

import (
	"context"
	"log/slog"

	"github.com/vincentiuslienardo/selatpay/internal/config"
	ldb "github.com/vincentiuslienardo/selatpay/internal/db"
	"github.com/vincentiuslienardo/selatpay/internal/outbox"
	"github.com/vincentiuslienardo/selatpay/internal/webhook"
)

// runDispatcher boots an outbox dispatcher for the intent.completed
// topic backed by the HMAC-signed webhook Sender. Per-merchant URL
// and signing secret come from the merchants table; an unconfigured
// merchant becomes a silent no-op so a single bad row can't stall
// the queue.
func runDispatcher(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	pool, err := ldb.OpenPool(ctx, cfg.DBURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	sender, err := webhook.NewSender(pool, webhook.Config{Log: logger})
	if err != nil {
		return err
	}

	d, err := outbox.NewDispatcher(pool, sender, outbox.DispatcherConfig{
		Topic: "intent.completed",
	}, logger)
	if err != nil {
		return err
	}

	logger.Info("dispatcher starting", "topic", "intent.completed")
	return d.Run(ctx)
}
