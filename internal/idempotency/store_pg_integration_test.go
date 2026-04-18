//go:build integration

package idempotency_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpg "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/vincentiuslienardo/selatpay/internal/idempotency"
)

func startPostgres(t *testing.T) (*pgxpool.Pool, func()) {
	t.Helper()
	ctx := context.Background()

	container, err := tcpg.Run(ctx,
		"postgres:16-alpine",
		tcpg.WithDatabase("selatpay_test"),
		tcpg.WithUsername("test"),
		tcpg.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(30*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("conn string: %v", err)
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	if err := applyMigrations(ctx, pool); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	return pool, func() {
		pool.Close()
		_ = container.Terminate(ctx)
	}
}

func applyMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	root, err := repoRoot()
	if err != nil {
		return err
	}
	dir := filepath.Join(root, "internal", "db", "migrations")
	files, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, f := range files {
		if f.IsDir() || !strings.HasSuffix(f.Name(), ".sql") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, f.Name()))
		if err != nil {
			return err
		}
		sql := stripGooseDown(string(raw))
		if _, err := pool.Exec(ctx, sql); err != nil {
			return fmt.Errorf("apply %s: %w", f.Name(), err)
		}
	}
	return nil
}

func stripGooseDown(s string) string {
	if i := strings.Index(s, "-- +goose Down"); i >= 0 {
		s = s[:i]
	}
	s = strings.ReplaceAll(s, "-- +goose Up", "")
	s = strings.ReplaceAll(s, "-- +goose StatementBegin", "")
	s = strings.ReplaceAll(s, "-- +goose StatementEnd", "")
	return s
}

func repoRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("go.mod not found")
		}
		dir = parent
	}
}

func TestPGStore_PutGetRoundtrip(t *testing.T) {
	pool, cleanup := startPostgres(t)
	defer cleanup()
	store := idempotency.NewPGStore(pool)
	ctx := context.Background()

	mid := uuid.New()
	rec := idempotency.Record{
		MerchantID:   mid,
		Key:          "k1",
		RequestHash:  idempotency.HashRequest([]byte(`{"a":1}`)),
		StatusCode:   201,
		ResponseBody: []byte(`{"id":"abc"}`),
	}

	stored, created, err := store.Put(ctx, rec)
	if err != nil || !created {
		t.Fatalf("first put: err=%v created=%v", err, created)
	}

	got, err := store.Get(ctx, mid, "k1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !bytes.Equal(got.RequestHash, rec.RequestHash) || got.StatusCode != 201 || string(got.ResponseBody) != `{"id":"abc"}` {
		t.Fatalf("got %+v, want %+v", got, stored)
	}
}

func TestPGStore_ConcurrentPutConvergesToWinner(t *testing.T) {
	pool, cleanup := startPostgres(t)
	defer cleanup()
	store := idempotency.NewPGStore(pool)
	ctx := context.Background()

	mid := uuid.New()
	const workers = 16

	var (
		wg         sync.WaitGroup
		mu         sync.Mutex
		createdN   int
		statuses   = make(map[int]int)
	)

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, created, err := store.Put(ctx, idempotency.Record{
				MerchantID:   mid,
				Key:          "race",
				RequestHash:  idempotency.HashRequest([]byte(fmt.Sprintf(`{"body":%d}`, i))),
				StatusCode:   200 + i,
				ResponseBody: []byte(fmt.Sprintf(`resp-%d`, i)),
			})
			if err != nil {
				t.Errorf("worker %d: %v", i, err)
				return
			}
			mu.Lock()
			if created {
				createdN++
			}
			mu.Unlock()
			_ = statuses
		}(i)
	}
	wg.Wait()

	if createdN != 1 {
		t.Fatalf("exactly one writer should report created=true; got %d", createdN)
	}

	final, err := store.Get(ctx, mid, "race")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if final.StatusCode < 200 || final.StatusCode > 200+workers {
		t.Fatalf("winner status out of range: %d", final.StatusCode)
	}
}

func TestPGStore_GetNotFound(t *testing.T) {
	pool, cleanup := startPostgres(t)
	defer cleanup()
	store := idempotency.NewPGStore(pool)
	ctx := context.Background()

	_, err := store.Get(ctx, uuid.New(), "nope")
	if !errors.Is(err, idempotency.ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}
