// Command mockidrbank is the standalone IDR-bank stub Selatpay's
// orchestrator points at in dev and integration tests. It honors
// X-Idempotency-Key for true dedup (same key → same cached
// response) and surfaces injectable failure modes via a ?fail=
// query parameter so a single binary can drive happy-path, retry,
// and permanent-failure scenarios.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
)

func main() {
	addr := getEnv("MOCK_BANK_ADDR", ":9100")
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	srv := newServer(logger)

	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           srv,
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		logger.Info("mock IDR bank listening", "addr", addr)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
		if err := <-errCh; err != nil {
			logger.Error("server exited with error", "err", err)
			os.Exit(1)
		}
	case err := <-errCh:
		if err != nil {
			logger.Error("server exited with error", "err", err)
			os.Exit(1)
		}
	}
}

type submitRequest struct {
	IntentID      string `json:"intent_id"`
	PayoutID      string `json:"payout_id"`
	AmountIDR     int64  `json:"amount_idr"`
	BankCode      string `json:"bank_code"`
	AccountNumber string `json:"account_number"`
	AccountName   string `json:"account_name"`
	Memo          string `json:"memo,omitempty"`
}

type submitResponse struct {
	Reference string `json:"reference"`
	Status    string `json:"status"`
	Message   string `json:"message,omitempty"`
}

// cachedResponse stores everything we need to replay a previous
// idempotent reply byte-for-byte.
type cachedResponse struct {
	statusCode int
	body       []byte
}

type server struct {
	mux *http.ServeMux
	log *slog.Logger

	mu    sync.Mutex
	cache map[string]cachedResponse
}

func newServer(log *slog.Logger) *server {
	s := &server{
		mux:   http.NewServeMux(),
		log:   log,
		cache: make(map[string]cachedResponse),
	}
	s.mux.HandleFunc("/payouts", s.handlePayouts)
	s.mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	return s
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

// handlePayouts implements the only endpoint Selatpay exercises. The
// happy path returns a synthetic reference; ?fail=retry returns 503
// and ?fail=permanent returns 422. Idempotency keys cache the entire
// response, so a saga retry sees the same outcome it saw the first
// time — including the same failure if the operator wants to test
// retry budgets.
func (s *server) handlePayouts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	idemKey := r.Header.Get("X-Idempotency-Key")
	if idemKey == "" {
		http.Error(w, "X-Idempotency-Key header required", http.StatusBadRequest)
		return
	}

	if cached := s.lookup(idemKey); cached != nil {
		s.write(w, cached.statusCode, cached.body)
		s.log.Info("cached reply",
			"idempotency_key", idemKey,
			"status", cached.statusCode,
		)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer func() { _ = r.Body.Close() }()

	var req submitRequest
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "decode body: "+err.Error(), http.StatusBadRequest)
			return
		}
	}

	statusCode, payload := s.decide(r.URL.Query().Get("fail"), req)
	respBytes, _ := json.Marshal(payload)
	s.store(idemKey, cachedResponse{statusCode: statusCode, body: respBytes})

	s.write(w, statusCode, respBytes)
	s.log.Info("payout decision",
		"idempotency_key", idemKey,
		"intent_id", req.IntentID,
		"payout_id", req.PayoutID,
		"amount_idr", req.AmountIDR,
		"status", statusCode,
	)
}

// decide picks the response shape based on the ?fail= knob. Empty or
// unknown fail values fall through to success so a misformed query
// in dev doesn't accidentally fail every payout.
func (s *server) decide(fail string, req submitRequest) (int, submitResponse) {
	switch fail {
	case "retry":
		return http.StatusServiceUnavailable, submitResponse{
			Status:  "retryable",
			Message: "downstream temporarily unavailable",
		}
	case "permanent":
		return http.StatusUnprocessableEntity, submitResponse{
			Status:  "rejected",
			Message: "account closed",
		}
	default:
		return http.StatusOK, submitResponse{
			Reference: "MIDR-" + uuid.NewString(),
			Status:    "succeeded",
			Message:   fmt.Sprintf("paid %d IDR to %s", req.AmountIDR, req.AccountNumber),
		}
	}
}

func (s *server) lookup(key string) *cachedResponse {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.cache[key]; ok {
		return &v
	}
	return nil
}

func (s *server) store(key string, v cachedResponse) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cache[key] = v
}

func (s *server) write(w http.ResponseWriter, statusCode int, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_, _ = w.Write(body)
}

func getEnv(k, def string) string {
	if v, ok := os.LookupEnv(k); ok {
		return v
	}
	return def
}
