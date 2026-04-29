package webhook

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	ldb "github.com/vincentiuslienardo/selatpay/internal/db"
	dbq "github.com/vincentiuslienardo/selatpay/internal/db/sqlc"
	"github.com/vincentiuslienardo/selatpay/internal/outbox"
)

// MerchantHeader names the outbox header that carries the merchant
// UUID. Producers (saga steps) set it; the Sender reads it to look
// up the destination URL and signing secret. Keeping merchant
// routing in headers means the Sender never has to parse the
// payload to find out who an event belongs to.
const MerchantHeader = "X-Selatpay-Merchant"

// Sender is the outbox.Sender that POSTs signed payloads to the
// merchant's webhook URL. Each message is one HTTP request,
// signed fresh with the merchant's secret and timestamp; the
// dispatcher's retry loop handles backoff for transient failures.
type Sender struct {
	pool   *pgxpool.Pool
	client *http.Client
	clock  func() time.Time
	log    *slog.Logger
}

// Config bundles the optional knobs. Zero values fall back to
// production-sane defaults inside NewSender.
type Config struct {
	Client *http.Client
	Clock  func() time.Time
	Log    *slog.Logger
}

func NewSender(pool *pgxpool.Pool, cfg Config) (*Sender, error) {
	if pool == nil {
		return nil, errors.New("webhook: pool is required")
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: 10 * time.Second}
	}
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	return &Sender{
		pool:   pool,
		client: cfg.Client,
		clock:  cfg.Clock,
		log:    cfg.Log,
	}, nil
}

// Send satisfies outbox.Sender. The contract: return nil for delivered
// messages, error for anything the dispatcher should treat as a retry.
// Missing merchant config is intentionally a non-error so the row
// gets marked delivered and the queue doesn't stall behind a single
// unconfigured merchant; a metric/log line surfaces the fact.
func (s *Sender) Send(ctx context.Context, msg outbox.Message) error {
	merchantIDStr := msg.Headers[MerchantHeader]
	if merchantIDStr == "" {
		s.log.Warn("webhook: outbox message missing merchant header",
			"id", msg.ID, "topic", msg.Topic)
		return nil
	}
	merchantID, err := uuid.Parse(merchantIDStr)
	if err != nil {
		s.log.Warn("webhook: outbox message has unparseable merchant id",
			"id", msg.ID, "topic", msg.Topic, "raw", merchantIDStr, "err", err)
		return nil
	}

	q := dbq.New(s.pool)
	cfg, err := q.GetMerchantWebhookConfig(ctx, ldb.PgUUID(merchantID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			s.log.Warn("webhook: merchant not found for delivery",
				"id", msg.ID, "merchant_id", merchantID)
			return nil
		}
		return fmt.Errorf("webhook: load config: %w", err)
	}
	if cfg.WebhookUrl == nil || *cfg.WebhookUrl == "" || len(cfg.WebhookSecret) == 0 {
		s.log.Info("webhook: skipping delivery, merchant has no webhook config",
			"id", msg.ID, "merchant_id", merchantID)
		return nil
	}

	signature, err := Sign(cfg.WebhookSecret, msg.Payload, s.clock())
	if err != nil {
		return fmt.Errorf("webhook: sign: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, *cfg.WebhookUrl, bytes.NewReader(msg.Payload))
	if err != nil {
		return fmt.Errorf("webhook: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(SignatureHeader, signature)
	for k, v := range msg.Headers {
		// Forward all producer-set headers (topic, idempotency
		// key, etc.) so the receiver's deduplication and routing
		// can use them. The merchant header is internal-only.
		if k == MerchantHeader {
			continue
		}
		req.Header.Set(k, v)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("webhook: post: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	// Drain the body so the connection can be reused; cap to a
	// small ceiling because a misbehaving receiver could otherwise
	// stream forever and stall the dispatcher.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 32<<10))

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		s.log.Info("webhook: delivered",
			"id", msg.ID, "merchant_id", merchantID, "status", resp.StatusCode)
		return nil
	}
	return fmt.Errorf("webhook: receiver responded %d", resp.StatusCode)
}
