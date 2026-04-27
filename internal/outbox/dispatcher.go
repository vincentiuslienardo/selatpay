package outbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	dbq "github.com/vincentiuslienardo/selatpay/internal/db/sqlc"
)

// DispatcherConfig bundles tunables for the outbox drain loop. Zero
// fields are filled in with production-sane defaults inside
// NewDispatcher.
type DispatcherConfig struct {
	Topic          string
	PollInterval   time.Duration
	IdleBackoff    time.Duration
	BatchSize      int32
	MaxAttempts    int32
	BackoffBase    time.Duration
	BackoffMax     time.Duration
	LockRetryEvery time.Duration
}

// Dispatcher drains a single outbox topic. Singleton ownership is
// enforced via a session-scoped Postgres advisory lock keyed on the
// topic string: at most one process can hold the lock at a time, and
// the lock auto-releases when its session dies, so a crashing
// dispatcher hands ownership over without manual intervention.
type Dispatcher struct {
	pool   *pgxpool.Pool
	sender Sender
	cfg    DispatcherConfig
	log    *slog.Logger
	rng    *rand.Rand
}

// NewDispatcher wires dependencies and applies defaults. Topic must be
// non-empty: it both keys the SQL claim and seeds the advisory-lock
// hash, so different topics drain in parallel without contention.
func NewDispatcher(pool *pgxpool.Pool, sender Sender, cfg DispatcherConfig, log *slog.Logger) (*Dispatcher, error) {
	if pool == nil {
		return nil, errors.New("outbox: pool is required")
	}
	if sender == nil {
		return nil, errors.New("outbox: sender is required")
	}
	if cfg.Topic == "" {
		return nil, errors.New("outbox: topic is required")
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 200 * time.Millisecond
	}
	if cfg.IdleBackoff <= 0 {
		cfg.IdleBackoff = 2 * time.Second
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 32
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 12
	}
	if cfg.BackoffBase <= 0 {
		cfg.BackoffBase = time.Second
	}
	if cfg.BackoffMax <= 0 {
		cfg.BackoffMax = 5 * time.Minute
	}
	if cfg.LockRetryEvery <= 0 {
		cfg.LockRetryEvery = 5 * time.Second
	}
	if log == nil {
		log = slog.Default()
	}
	return &Dispatcher{
		pool:   pool,
		sender: sender,
		cfg:    cfg,
		log:    log,
		rng:    rand.New(rand.NewSource(time.Now().UnixNano())),
	}, nil
}

// Run blocks until ctx fires. The outer loop keeps reacquiring the
// advisory lock if the inner dispatch loop ever exits unexpectedly
// (connection dropped, transient SQL error). The lock is released
// implicitly when the held connection is returned to the pool.
func (d *Dispatcher) Run(ctx context.Context) error {
	d.log.Info("outbox dispatcher starting", "topic", d.cfg.Topic)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		conn, err := d.acquireWithLock(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return err
			}
			return fmt.Errorf("outbox: acquire lock: %w", err)
		}

		runErr := d.runOwnedLoop(ctx, conn)

		// Release lock on whatever context we have, but use a
		// background context if ctx is already done so the unlock
		// query still gets a chance to run.
		releaseCtx := ctx
		if ctx.Err() != nil {
			var cancel context.CancelFunc
			releaseCtx, cancel = context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
		}
		d.releaseLock(releaseCtx, conn)
		conn.Release()

		if errors.Is(runErr, context.Canceled) {
			return runErr
		}
		if runErr != nil {
			d.log.Warn("dispatcher loop exited; reacquiring lock", "err", runErr)
		}
	}
}

// acquireWithLock claims one pool connection and polls
// pg_try_advisory_lock until it succeeds. We hold the connection for
// the dispatcher's lifetime so the session-scoped lock survives;
// returning the connection to the pool would release the lock.
func (d *Dispatcher) acquireWithLock(ctx context.Context) (*pgxpool.Conn, error) {
	for {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		conn, err := d.pool.Acquire(ctx)
		if err != nil {
			return nil, err
		}
		ok, err := d.tryLock(ctx, conn)
		if err != nil {
			conn.Release()
			return nil, err
		}
		if ok {
			d.log.Info("outbox dispatcher acquired topic lock", "topic", d.cfg.Topic)
			return conn, nil
		}
		// Another dispatcher holds the lock. Sleep and try again.
		conn.Release()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(d.cfg.LockRetryEvery):
		}
	}
}

// tryLock attempts to grab a session-scoped advisory lock keyed on
// 'outbox.dispatcher.' + topic. We hash to bigint inside SQL so the
// caller doesn't have to know how Postgres derives the lock key from
// the topic string.
func (d *Dispatcher) tryLock(ctx context.Context, conn *pgxpool.Conn) (bool, error) {
	key := "outbox.dispatcher." + d.cfg.Topic
	var ok bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock(hashtext($1)::bigint)", key).Scan(&ok); err != nil {
		return false, err
	}
	return ok, nil
}

func (d *Dispatcher) releaseLock(ctx context.Context, conn *pgxpool.Conn) {
	key := "outbox.dispatcher." + d.cfg.Topic
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_unlock(hashtext($1)::bigint)", key); err != nil {
		d.log.Warn("outbox: advisory unlock failed", "topic", d.cfg.Topic, "err", err)
	}
}

// runOwnedLoop runs the dispatch loop on a connection we own and that
// holds the topic's advisory lock. Returning here releases the lock,
// which the outer loop treats as a signal to reacquire from the top.
func (d *Dispatcher) runOwnedLoop(ctx context.Context, conn *pgxpool.Conn) error {
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		didWork, err := d.drainOnce(ctx, conn)
		if err != nil {
			return err
		}
		wait := d.cfg.PollInterval
		if !didWork {
			wait = d.cfg.IdleBackoff
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}

// drainOnce claims a batch and dispatches each message inside its own
// short transaction. Per-message tx scope means a slow Sender can't
// hold a row lock across the whole batch and stop a parallel claim
// from advancing.
func (d *Dispatcher) drainOnce(ctx context.Context, conn *pgxpool.Conn) (bool, error) {
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, fmt.Errorf("begin claim tx: %w", err)
	}
	q := dbq.New(tx)
	rows, err := q.ClaimDueOutbox(ctx, dbq.ClaimDueOutboxParams{
		Topic: d.cfg.Topic,
		Limit: d.cfg.BatchSize,
	})
	if err != nil {
		_ = tx.Rollback(ctx)
		return false, fmt.Errorf("claim batch: %w", err)
	}
	if len(rows) == 0 {
		_ = tx.Rollback(ctx)
		return false, nil
	}

	// We resolve the result of every message inside this same tx —
	// either MarkDelivered or ScheduleRetry — which clears the row
	// lock on commit. If the dispatcher dies before commit, the
	// claimed rows simply revert to claimable on the next iteration.
	for _, row := range rows {
		msg, err := toMessage(row)
		if err != nil {
			d.log.Error("decode outbox row", "id", row.ID, "err", err)
			continue
		}
		sendErr := d.sender.Send(ctx, msg)
		if sendErr == nil {
			if _, err := q.MarkOutboxDelivered(ctx, row.ID); err != nil {
				_ = tx.Rollback(ctx)
				return false, fmt.Errorf("mark delivered: %w", err)
			}
			d.log.Info("outbox delivered", "id", msg.ID, "topic", msg.Topic)
			continue
		}

		// Compute retry schedule. If the next attempt would exceed
		// MaxAttempts, push next_attempt_at far enough out that the
		// dispatcher won't pick the row back up; an ops endpoint can
		// inspect last_error and either reset or move the row to a
		// dead-letter store.
		attempt := row.Attempts + 1
		var delay time.Duration
		if attempt >= d.cfg.MaxAttempts {
			delay = 100 * 365 * 24 * time.Hour
			d.log.Error("outbox exhausted retries", "id", msg.ID, "topic", msg.Topic, "attempts", attempt, "err", sendErr)
		} else {
			delay = d.computeBackoff(attempt)
			d.log.Warn("outbox retrying", "id", msg.ID, "topic", msg.Topic, "attempt", attempt, "delay", delay, "err", sendErr)
		}
		errMsg := sendErr.Error()
		if _, err := q.ScheduleOutboxRetry(ctx, dbq.ScheduleOutboxRetryParams{
			ID:            row.ID,
			NextAttemptAt: pgtype.Timestamptz{Time: time.Now().Add(delay), Valid: true},
			LastError:     &errMsg,
		}); err != nil {
			_ = tx.Rollback(ctx)
			return false, fmt.Errorf("schedule retry: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit drain: %w", err)
	}
	return true, nil
}

// computeBackoff applies exponential growth with full jitter, capped
// at BackoffMax. Same shape as the saga runner's backoff, kept inline
// so the outbox package stays import-light.
func (d *Dispatcher) computeBackoff(attempt int32) time.Duration {
	candidate := d.cfg.BackoffBase
	for i := int32(1); i < attempt && candidate < d.cfg.BackoffMax; i++ {
		candidate *= 2
	}
	if candidate > d.cfg.BackoffMax {
		candidate = d.cfg.BackoffMax
	}
	if candidate <= 0 {
		return 0
	}
	return time.Duration(d.rng.Int63n(int64(candidate)))
}
