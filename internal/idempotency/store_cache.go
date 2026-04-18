package idempotency

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// CachedStore wraps a durable Store with a Redis read-through cache. Redis is
// treated as best-effort — any cache error falls through to the underlying
// store so the system degrades to correct-but-slower under Redis outages.
type CachedStore struct {
	inner Store
	rdb   *redis.Client
	ttl   time.Duration
}

func NewCachedStore(inner Store, rdb *redis.Client, ttl time.Duration) *CachedStore {
	return &CachedStore{inner: inner, rdb: rdb, ttl: ttl}
}

type cachePayload struct {
	RequestHash  []byte `json:"h"`
	StatusCode   int    `json:"s"`
	ResponseBody []byte `json:"b"`
}

func cacheKey(merchantID uuid.UUID, key string) string {
	return "idemp:" + merchantID.String() + ":" + key
}

func (c *CachedStore) Get(ctx context.Context, merchantID uuid.UUID, key string) (Record, error) {
	ck := cacheKey(merchantID, key)
	if raw, err := c.rdb.Get(ctx, ck).Bytes(); err == nil {
		var p cachePayload
		if jerr := json.Unmarshal(raw, &p); jerr == nil {
			return Record{
				MerchantID:   merchantID,
				Key:          key,
				RequestHash:  p.RequestHash,
				StatusCode:   p.StatusCode,
				ResponseBody: p.ResponseBody,
			}, nil
		}
	} else if !errors.Is(err, redis.Nil) {
		// transient cache error — log-and-continue; don't fail the request
		// just because Redis hiccupped. The store is truth.
	}

	rec, err := c.inner.Get(ctx, merchantID, key)
	if err != nil {
		return Record{}, err
	}
	c.warm(ctx, ck, rec)
	return rec, nil
}

func (c *CachedStore) Put(ctx context.Context, r Record) (Record, bool, error) {
	stored, created, err := c.inner.Put(ctx, r)
	if err != nil {
		return Record{}, false, err
	}
	c.warm(ctx, cacheKey(stored.MerchantID, stored.Key), stored)
	return stored, created, nil
}

func (c *CachedStore) warm(ctx context.Context, ck string, r Record) {
	b, err := json.Marshal(cachePayload{
		RequestHash:  r.RequestHash,
		StatusCode:   r.StatusCode,
		ResponseBody: r.ResponseBody,
	})
	if err != nil {
		return
	}
	if err := c.rdb.Set(ctx, ck, b, c.ttl).Err(); err != nil {
		_ = fmt.Errorf("redis set: %w", err) // intentionally swallowed; cache is best-effort
	}
}
