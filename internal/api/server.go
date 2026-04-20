package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/vincentiuslienardo/selatpay/internal/api/apispec"
	"github.com/vincentiuslienardo/selatpay/internal/auth"
	"github.com/vincentiuslienardo/selatpay/internal/idempotency"
	"github.com/vincentiuslienardo/selatpay/internal/quoter"
)

type Deps struct {
	Pool          *pgxpool.Pool
	Redis         *redis.Client
	Quoter        *quoter.Quoter
	KeyStore      auth.KeyStore
	IdempotencyTTL time.Duration
	Now           func() time.Time
}

// NewRouter builds the chi router with middleware chain for the api subcommand.
// The order matters: request id → logger → recoverer → auth (sets merchant on
// ctx) → idempotency (uses merchant from ctx) → generated route handlers.
func NewRouter(d Deps) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)

	// Public routes first — no auth, no idempotency.
	r.Get("/healthz", (&Handlers{Pool: d.Pool, Quoter: d.Quoter}).Healthz)

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

		pr.Mount("/", apispec.Handler(&Handlers{Pool: d.Pool, Quoter: d.Quoter}))
	})
	return r
}
