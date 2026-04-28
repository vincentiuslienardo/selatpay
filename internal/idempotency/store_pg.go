package idempotency

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	ldb "github.com/vincentiuslienardo/selatpay/internal/db"
	dbq "github.com/vincentiuslienardo/selatpay/internal/db/sqlc"
)

type PGStore struct {
	pool *pgxpool.Pool
}

func NewPGStore(pool *pgxpool.Pool) *PGStore { return &PGStore{pool: pool} }

func (s *PGStore) Get(ctx context.Context, merchantID uuid.UUID, key string) (Record, error) {
	q := dbq.New(s.pool)
	row, err := q.GetIdempotencyKey(ctx, dbq.GetIdempotencyKeyParams{
		MerchantID: ldb.PgUUID(merchantID),
		Key:        key,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Record{}, ErrNotFound
		}
		return Record{}, fmt.Errorf("get idempotency: %w", err)
	}
	return Record{
		MerchantID:   ldb.FromPgUUID(row.MerchantID),
		Key:          row.Key,
		RequestHash:  row.RequestHash,
		StatusCode:   int(row.StatusCode),
		ResponseBody: row.ResponseBody,
	}, nil
}

func (s *PGStore) Put(ctx context.Context, r Record) (Record, bool, error) {
	q := dbq.New(s.pool)
	inserted, err := q.InsertIdempotencyKey(ctx, dbq.InsertIdempotencyKeyParams{
		MerchantID:  ldb.PgUUID(r.MerchantID),
		Key:         r.Key,
		RequestHash: r.RequestHash,
		// HTTP status codes are bounded to the 100..599 range; the int32
		// cast here is a no-op for any value the middleware can produce.
		StatusCode:   int32(r.StatusCode), //nolint:gosec // see comment above
		ResponseBody: r.ResponseBody,
	})
	if err == nil {
		return Record{
			MerchantID:   ldb.FromPgUUID(inserted.MerchantID),
			Key:          inserted.Key,
			RequestHash:  inserted.RequestHash,
			StatusCode:   int(inserted.StatusCode),
			ResponseBody: inserted.ResponseBody,
		}, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Record{}, false, fmt.Errorf("insert idempotency: %w", err)
	}
	// ON CONFLICT DO NOTHING returns no row — another writer won. Fetch theirs.
	existing, gerr := s.Get(ctx, r.MerchantID, r.Key)
	if gerr != nil {
		return Record{}, false, fmt.Errorf("fetch conflict winner: %w", gerr)
	}
	return existing, false, nil
}
