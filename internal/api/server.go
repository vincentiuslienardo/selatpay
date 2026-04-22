package api

import (
	"net/http"
	"time"

	"github.com/gagliardetto/solana-go"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/vincentiuslienardo/selatpay/internal/api/apispec"
	"github.com/vincentiuslienardo/selatpay/internal/auth"
	"github.com/vincentiuslienardo/selatpay/internal/idempotency"
	"github.com/vincentiuslienardo/selatpay/internal/quoter"
	"github.com/vincentiuslienardo/selatpay/internal/solanapay"
)

type Deps struct {
	Pool           *pgxpool.Pool
	Redis          *redis.Client
	Quoter         *quoter.Quoter
	KeyStore       auth.KeyStore
	IdempotencyTTL time.Duration
	Now            func() time.Time

	// Allocator mints fresh reference keypairs for Solana Pay intents and
	// seals the private key for at-rest storage. Required.
	Allocator *solanapay.Allocator

	// HotWalletPubkey is the transfer recipient embedded in every Solana
	// Pay URL. Wallets derive the destination ATA from (pubkey, USDCMint).
	HotWalletPubkey solana.PublicKey

	// USDCMint is the SPL mint the intent is denominated in (USDC on
	// devnet or mainnet). USDCDecimals is typically 6.
	USDCMint     solana.PublicKey
	USDCDecimals uint8

	// SolanaPayLabel and SolanaPayMessage populate the URL's label/message
	// parameters so the payer's wallet shows a friendly label rather than
	// a bare pubkey. SolanaPayMessage is a static string; per-intent detail
	// (external_ref) is appended automatically when SolanaPayMessage is
	// empty.
	SolanaPayLabel   string
	SolanaPayMessage string
}

// NewRouter builds the chi router with middleware chain for the api subcommand.
// The order matters: request id → logger → recoverer → auth (sets merchant on
// ctx) → idempotency (uses merchant from ctx) → generated route handlers.
func NewRouter(d Deps) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)

	h := &Handlers{
		Pool:             d.Pool,
		Quoter:           d.Quoter,
		Allocator:        d.Allocator,
		HotWalletPubkey:  d.HotWalletPubkey,
		USDCMint:         d.USDCMint,
		USDCDecimals:     d.USDCDecimals,
		SolanaPayLabel:   d.SolanaPayLabel,
		SolanaPayMessage: d.SolanaPayMessage,
	}

	// Public routes first — no auth, no idempotency.
	r.Get("/healthz", h.Healthz)

	r.Group(func(pr chi.Router) {
		pr.Use(auth.Middleware(d.KeyStore, d.Now))

		var store idempotency.Store = idempotency.NewPGStore(d.Pool)
		if d.Redis != nil {
			ttl := d.IdempotencyTTL
			if ttl <= 0 {
				ttl = 24 * time.Hour
			}
			store = idempotency.NewCachedStore(store, d.Redis, ttl)
		}
		idem := idempotency.NewMiddleware(store, func(req *http.Request) (uuid.UUID, bool) {
			return auth.MerchantFromContext(req.Context())
		})
		pr.Use(idem.Handler)

		pr.Mount("/", apispec.Handler(h))
	})
	return r
}
