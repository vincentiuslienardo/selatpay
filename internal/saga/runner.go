package saga

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	ldb "github.com/vincentiuslienardo/selatpay/internal/db"
	dbq "github.com/vincentiuslienardo/selatpay/internal/db/sqlc"
)

// Config bundles tunables for the orchestrator loop. Any zero field is
// replaced by a production-sane default in NewRunner so callers only
// have to override what they care about.
type Config struct {
	Owner         string
	PollInterval  time.Duration
	IdleBackoff   time.Duration
	LeaseDuration time.Duration
	MaxAttempts   int32
	BackoffBase   time.Duration
	BackoffMax    time.Duration
}

// Runner is a single orchestrator worker. Multiple Runner instances can
// coexist against one Postgres — claim contention is handled by FOR
// UPDATE SKIP LOCKED in the SQL, and time-bound leases bound how long a
// crashed worker can squat on a saga.
type Runner struct {
	pool     *pgxpool.Pool
	registry *Registry
	cfg      Config
	backoff  *Backoff
	log      *slog.Logger
}

// NewRunner wires dependencies and applies defaults. Owner identifies
// this worker in saga_runs.lease_owner; passing the empty string is a
// programming error — observability and forensics rely on knowing which
// process held the lease at any given moment.
func NewRunner(pool *pgxpool.Pool, registry *Registry, cfg Config, log *slog.Logger) (*Runner, error) {
	if pool == nil {
		return nil, errors.New("saga: pool is required")
	}
	if registry == nil {
		return nil, errors.New("saga: registry is required")
	}
	if cfg.Owner == "" {
		return nil, errors.New("saga: owner is required")
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 500 * time.Millisecond
	}
	if cfg.IdleBackoff <= 0 {
		cfg.IdleBackoff = 2 * time.Second
	}
	if cfg.LeaseDuration <= 0 {
		cfg.LeaseDuration = 60 * time.Second
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 8
	}
	if cfg.BackoffBase <= 0 {
		cfg.BackoffBase = time.Second
	}
	if cfg.BackoffMax <= 0 {
		cfg.BackoffMax = 5 * time.Minute
	}
	if log == nil {
		log = slog.Default()
	}
	return &Runner{
		pool:     pool,
		registry: registry,
		cfg:      cfg,
		backoff:  NewBackoff(cfg.BackoffBase, cfg.BackoffMax),
		log:      log,
	}, nil
}

// Run blocks until ctx fires. It loops claim → execute → advance with a
// short interval when work is found and a longer back-off when the
// queue is idle, so the worker doesn't hammer Postgres with no-op CTEs
// when nothing is due.
func (r *Runner) Run(ctx context.Context) error {
	r.log.Info("saga runner starting", "owner", r.cfg.Owner, "poll", r.cfg.PollInterval)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		didWork, err := r.Tick(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			r.log.Warn("saga tick failed", "err", err)
		}
		wait := r.cfg.PollInterval
		if !didWork {
			wait = r.cfg.IdleBackoff
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}

// Tick runs at most one saga step. It returns didWork=true when a saga
// was claimed (regardless of step success), so the caller can poll fast
// while the queue is busy and back off when it drains. Exposed for
// tests so they can drive the runner deterministically without spinning
// up Run's goroutine timing.
func (r *Runner) Tick(ctx context.Context) (bool, error) {
	claimed, err := r.claim(ctx)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("claim: %w", err)
	}

	step, ok := r.registry.Get(claimed.CurrentStep)
	if !ok {
		// Unknown step name is a deploy/config bug, not a transient
		// fault — fail terminally rather than retry.
		r.failRun(ctx, claimed.ID, fmt.Errorf("unknown step %q", claimed.CurrentStep))
		return true, nil
	}

	stepErr := r.executeStep(ctx, step, claimed)
	if stepErr == nil {
		return true, nil
	}

	if isTerminal(stepErr) || claimed.StepAttempts+1 >= r.cfg.MaxAttempts {
		r.failRun(ctx, claimed.ID, stepErr)
		return true, nil
	}

	delay := r.backoff.Delay(claimed.StepAttempts + 1)
	r.scheduleRetry(ctx, claimed.ID, delay, stepErr)
	return true, nil
}

// claim leases one due saga in its own short transaction and returns
// the captured Run. We hold the lease via lease_until rather than the
// row lock, so the long-running step transaction below cannot block
// other workers from claiming siblings.
func (r *Runner) claim(ctx context.Context) (Run, error) {
	q := dbq.New(r.pool)
	leaseSeconds := r.cfg.LeaseDuration.Seconds()
	row, err := q.ClaimDueSagaRun(ctx, dbq.ClaimDueSagaRunParams{
		LeaseOwner:   r.cfg.Owner,
		LeaseSeconds: leaseSeconds,
	})
	if err != nil {
		return Run{}, err
	}
	return toRun(row), nil
}

// executeStep runs a step inside its own transaction and advances saga
// state in the same transaction. Any error the step returns rolls back
// every effect, including the advance — so a partial step never sees
// the saga move forward in saga_runs.
func (r *Runner) executeStep(ctx context.Context, step Step, run Run) error {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	result, err := step.Execute(ctx, tx, run)
	if err != nil {
		return fmt.Errorf("step %s: %w", step.Name(), err)
	}

	q := dbq.New(tx)
	if result.NextStep == "" {
		if _, err := q.CompleteSagaRun(ctx, ldb.PgUUID(run.ID)); err != nil {
			return fmt.Errorf("complete saga: %w", err)
		}
	} else {
		if _, err := q.AdvanceSagaStep(ctx, dbq.AdvanceSagaStepParams{
			ID:          ldb.PgUUID(run.ID),
			CurrentStep: result.NextStep,
		}); err != nil {
			return fmt.Errorf("advance saga: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	r.log.Info("saga step completed",
		"saga_id", run.ID,
		"step", step.Name(),
		"result", result.String(),
	)
	return nil
}

func (r *Runner) failRun(ctx context.Context, id uuid.UUID, cause error) {
	msg := cause.Error()
	q := dbq.New(r.pool)
	if _, err := q.FailSagaRun(ctx, dbq.FailSagaRunParams{
		ID:        ldb.PgUUID(id),
		LastError: &msg,
	}); err != nil {
		r.log.Error("saga: fail update failed", "saga_id", id, "err", err)
		return
	}
	r.log.Error("saga failed", "saga_id", id, "cause", msg)
}

func (r *Runner) scheduleRetry(ctx context.Context, id uuid.UUID, delay time.Duration, cause error) {
	msg := cause.Error()
	next := time.Now().Add(delay)
	q := dbq.New(r.pool)
	if _, err := q.ScheduleSagaRetry(ctx, dbq.ScheduleSagaRetryParams{
		ID:        ldb.PgUUID(id),
		NextRunAt: pgtype.Timestamptz{Time: next, Valid: true},
		LastError: &msg,
	}); err != nil {
		r.log.Error("saga: retry update failed", "saga_id", id, "err", err)
		return
	}
	r.log.Warn("saga retrying",
		"saga_id", id,
		"delay", delay,
		"cause", msg,
	)
}

func toRun(row dbq.SagaRun) Run {
	owner := ""
	if row.LeaseOwner != nil {
		owner = *row.LeaseOwner
	}
	return Run{
		ID:           ldb.FromPgUUID(row.ID),
		IntentID:     ldb.FromPgUUID(row.IntentID),
		SagaKind:     row.SagaKind,
		CurrentStep:  row.CurrentStep,
		StepAttempts: row.StepAttempts,
		LeaseOwner:   owner,
		CreatedAt:    row.CreatedAt.Time,
	}
}
