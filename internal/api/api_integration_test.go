//go:build integration

package api_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpg "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/vincentiuslienardo/selatpay/internal/api"
	"github.com/vincentiuslienardo/selatpay/internal/api/apispec"
	"github.com/vincentiuslienardo/selatpay/internal/auth"
	ldb "github.com/vincentiuslienardo/selatpay/internal/db"
	dbq "github.com/vincentiuslienardo/selatpay/internal/db/sqlc"
	"github.com/vincentiuslienardo/selatpay/internal/quoter"
)

type fixture struct {
	t          *testing.T
	pool       *pgxpool.Pool
	server     *httptest.Server
	merchantID uuid.UUID
	keyID      string
	rawSecret  []byte
}

func startFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()

	container, err := tcpg.Run(ctx,
		"postgres:16-alpine",
		tcpg.WithDatabase("selatpay_api"),
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

	pepper := []byte("int-test-pepper")
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatalf("rand: %v", err)
	}

	q := dbq.New(pool)
	merchant, err := q.CreateMerchant(ctx, "acme")
	if err != nil {
		t.Fatalf("create merchant: %v", err)
	}
	keyID := "kid_test_" + uuid.NewString()
	if _, err := q.CreateAPIKey(ctx, dbq.CreateAPIKeyParams{
		MerchantID: merchant.ID,
		KeyID:      keyID,
		SecretHash: auth.DeriveSecret(pepper, raw),
	}); err != nil {
		t.Fatalf("create api key: %v", err)
	}

	router := api.NewRouter(api.Deps{
		Pool: pool,
		Quoter: quoter.New(pool, quoter.NewMockProvider(quoter.DefaultMockRates()), []byte("quote-sk"), quoter.Options{
			TTL:       time.Minute,
			SpreadBps: 50,
		}),
		KeyStore: auth.NewPGKeyStore(pool, pepper),
	})
	srv := httptest.NewServer(router)

	t.Cleanup(func() {
		srv.Close()
		pool.Close()
		_ = container.Terminate(ctx)
	})

	return &fixture{
		t:          t,
		pool:       pool,
		server:     srv,
		merchantID: ldb.FromPgUUID(merchant.ID),
		keyID:      keyID,
		rawSecret:  auth.DeriveSecret(pepper, raw),
	}
}

func (f *fixture) do(method, path string, body any, headers map[string]string) *http.Response {
	f.t.Helper()
	var buf []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			f.t.Fatalf("marshal: %v", err)
		}
		buf = b
	}
	req, err := http.NewRequest(method, f.server.URL+path, bytes.NewReader(buf))
	if err != nil {
		f.t.Fatalf("new req: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	ts := strconv.FormatInt(time.Now().Unix(), 10)
	req.Header.Set(auth.HeaderKeyID, f.keyID)
	req.Header.Set(auth.HeaderTimestamp, ts)
	req.Header.Set(auth.HeaderSignature, hex.EncodeToString(auth.Sign(f.rawSecret, method, path, ts, buf)))

	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatalf("http: %v", err)
	}
	return resp
}

func TestHealthz_Public(t *testing.T) {
	f := startFixture(t)
	resp, err := http.Get(f.server.URL + "/healthz")
	if err != nil {
		t.Fatalf("healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("healthz code: %d", resp.StatusCode)
	}
}

func TestCreatePaymentIntent_EndToEnd(t *testing.T) {
	f := startFixture(t)

	body := apispec.CreatePaymentIntentRequest{
		ExternalRef: "order-" + uuid.NewString(),
		AmountIdr:   16_200_000,
	}
	resp := f.do(http.MethodPost, "/v1/payment_intents", body, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("create: %d — %s", resp.StatusCode, data)
	}

	var intent apispec.PaymentIntent
	if err := json.NewDecoder(resp.Body).Decode(&intent); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if uuid.UUID(intent.MerchantId) != f.merchantID {
		t.Fatalf("merchant id mismatch")
	}
	if intent.State != apispec.Pending {
		t.Fatalf("state=%s, want pending", intent.State)
	}
	if intent.QuotedAmountUsdc <= 0 {
		t.Fatalf("quoted_amount_usdc must be positive, got %d", intent.QuotedAmountUsdc)
	}
	if intent.Quote.Pair != quoter.PairUSDCIDR {
		t.Fatalf("quote pair: %s", intent.Quote.Pair)
	}
	if time.Until(intent.Quote.ExpiresAt) <= 0 {
		t.Fatalf("quote must not be expired on issuance")
	}

	// Second create with same external_ref returns the same intent (200, not 201).
	resp2 := f.do(http.MethodPost, "/v1/payment_intents", body, nil)
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp2.Body)
		t.Fatalf("re-create code: %d — %s", resp2.StatusCode, data)
	}
	var intent2 apispec.PaymentIntent
	if err := json.NewDecoder(resp2.Body).Decode(&intent2); err != nil {
		t.Fatalf("decode 2: %v", err)
	}
	if intent2.Id != intent.Id {
		t.Fatalf("external_ref idempotency broken: %s vs %s", intent.Id, intent2.Id)
	}
}

func TestIdempotencyHeader_ReplayIsDeterministic(t *testing.T) {
	f := startFixture(t)

	idemKey := "idem-" + uuid.NewString()
	body := apispec.CreatePaymentIntentRequest{
		ExternalRef: "order-" + uuid.NewString(),
		AmountIdr:   5_000_000,
	}

	resp1 := f.do(http.MethodPost, "/v1/payment_intents", body, map[string]string{"Idempotency-Key": idemKey})
	raw1, _ := io.ReadAll(resp1.Body)
	resp1.Body.Close()
	if resp1.StatusCode != http.StatusCreated {
		t.Fatalf("first: %d — %s", resp1.StatusCode, raw1)
	}

	resp2 := f.do(http.MethodPost, "/v1/payment_intents", body, map[string]string{"Idempotency-Key": idemKey})
	raw2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusCreated {
		t.Fatalf("replay: %d — %s", resp2.StatusCode, raw2)
	}
	if resp2.Header.Get("Idempotent-Replay") != "true" {
		t.Fatalf("replay missing Idempotent-Replay: true header")
	}
	if string(raw1) != string(raw2) {
		t.Fatalf("replay body diverged:\n  first: %s\n  second: %s", raw1, raw2)
	}
}

func TestIdempotencyHeader_BodyMismatchReturns409(t *testing.T) {
	f := startFixture(t)
	idemKey := "idem-" + uuid.NewString()

	b1 := apispec.CreatePaymentIntentRequest{ExternalRef: "o-" + uuid.NewString(), AmountIdr: 1000}
	f.do(http.MethodPost, "/v1/payment_intents", b1, map[string]string{"Idempotency-Key": idemKey}).Body.Close()

	b2 := apispec.CreatePaymentIntentRequest{ExternalRef: "different", AmountIdr: 1000}
	resp := f.do(http.MethodPost, "/v1/payment_intents", b2, map[string]string{"Idempotency-Key": idemKey})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("got %d, want 409", resp.StatusCode)
	}
}

func TestUnauthenticated_Returns401(t *testing.T) {
	f := startFixture(t)

	resp, err := http.Post(
		f.server.URL+"/v1/payment_intents",
		"application/json",
		strings.NewReader(`{"external_ref":"x","amount_idr":1}`),
	)
	if err != nil {
		t.Fatalf("http: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", resp.StatusCode)
	}
}

func TestCrossMerchantGet_Returns404(t *testing.T) {
	f := startFixture(t)

	body := apispec.CreatePaymentIntentRequest{ExternalRef: "o-" + uuid.NewString(), AmountIdr: 1000}
	resp := f.do(http.MethodPost, "/v1/payment_intents", body, nil)
	var intent apispec.PaymentIntent
	_ = json.NewDecoder(resp.Body).Decode(&intent)
	resp.Body.Close()

	// Provision a second merchant with a separate key; GET the first merchant's
	// intent via the second merchant's credentials — must 404, never 200.
	ctx := context.Background()
	q := dbq.New(f.pool)
	other, err := q.CreateMerchant(ctx, "other")
	if err != nil {
		t.Fatalf("create merchant: %v", err)
	}
	rawSecret := make([]byte, 32)
	_, _ = rand.Read(rawSecret)
	otherKeyID := "kid_other_" + uuid.NewString()
	pepper := []byte("int-test-pepper")
	if _, err := q.CreateAPIKey(ctx, dbq.CreateAPIKeyParams{
		MerchantID: other.ID,
		KeyID:      otherKeyID,
		SecretHash: auth.DeriveSecret(pepper, rawSecret),
	}); err != nil {
		t.Fatalf("create other api key: %v", err)
	}

	path := "/v1/payment_intents/" + intent.Id.String()
	req, _ := http.NewRequest(http.MethodGet, f.server.URL+path, nil)
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	req.Header.Set(auth.HeaderKeyID, otherKeyID)
	req.Header.Set(auth.HeaderTimestamp, ts)
	req.Header.Set(auth.HeaderSignature, hex.EncodeToString(auth.Sign(auth.DeriveSecret(pepper, rawSecret), http.MethodGet, path, ts, nil)))

	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("http: %v", err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-tenant leak: got %d, want 404", r.StatusCode)
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
