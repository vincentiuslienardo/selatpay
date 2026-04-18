package idempotency

import (
	"context"
	"crypto/sha256"
	"errors"

	"github.com/google/uuid"
)

var (
	ErrNotFound     = errors.New("idempotency: key not found")
	ErrHashMismatch = errors.New("idempotency: request body differs from original for this key")
)

// Record is the full replayable response tied to an idempotency key.
type Record struct {
	MerchantID   uuid.UUID
	Key          string
	RequestHash  []byte
	StatusCode   int
	ResponseBody []byte
}

// Store is the durable-truth store. Implementations must treat (merchant, key)
// as the primary key and must be safe under concurrent callers.
type Store interface {
	// Get returns ErrNotFound when no record exists.
	Get(ctx context.Context, merchantID uuid.UUID, key string) (Record, error)
	// Put inserts a record. Returns the winning record; the `created` flag is
	// false when a concurrent writer beat us and the stored record is returned
	// instead of ours.
	Put(ctx context.Context, r Record) (stored Record, created bool, err error)
}

// HashRequest returns a SHA-256 over the request body. Used to detect clients
// that reuse an idempotency key with a different payload.
func HashRequest(body []byte) []byte {
	sum := sha256.Sum256(body)
	return sum[:]
}
