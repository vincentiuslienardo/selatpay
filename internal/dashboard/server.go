// Package dashboard renders a read-only htmx + Go templates view of
// the operational state every other Selatpay component writes to:
// payment intents, the on-chain payments they tracked, the saga
// state machine, ledger postings, payouts, and the outbox queue.
// It is deliberately read-only so an operator dropping in mid-flow
// cannot accidentally mutate state from a browser.
package dashboard

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	ldb "github.com/vincentiuslienardo/selatpay/internal/db"
	dbq "github.com/vincentiuslienardo/selatpay/internal/db/sqlc"
)

//go:embed templates/*.html
var templatesFS embed.FS

// Server bundles the chi router and the parsed templates. One
// instance per process; safe for concurrent use because templates
// and the pool both are.
type Server struct {
	pool      *pgxpool.Pool
	templates *template.Template
	mux       *chi.Mux
}

// NewServer parses the embedded templates and wires the routes.
// Failing to parse templates at boot is intentional: a typo in a
// template should crash the dashboard rather than 500 every page.
func NewServer(pool *pgxpool.Pool) (*Server, error) {
	if pool == nil {
		return nil, errors.New("dashboard: pool is required")
	}
	tmpl, err := template.New("").Funcs(funcMap()).ParseFS(templatesFS, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("dashboard: parse templates: %w", err)
	}
	s := &Server{
		pool:      pool,
		templates: tmpl,
		mux:       chi.NewMux(),
	}
	s.routes()
	return s, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

func (s *Server) routes() {
	s.mux.Get("/", s.handleIndex)
	s.mux.Get("/intents/{id}", s.handleIntent)
	s.mux.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	limit := int32(50)
	if v := r.URL.Query().Get("limit"); v != "" {
		// Parse as int32 directly so gosec sees the upper bound at
		// the parse site rather than an unbounded int conversion.
		if n, err := strconv.ParseInt(v, 10, 32); err == nil && n > 0 && n <= 500 {
			limit = int32(n)
		}
	}

	q := dbq.New(s.pool)
	rows, err := q.ListPaymentIntentsRecent(r.Context(), limit)
	if err != nil {
		http.Error(w, "list intents: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, "index.html", map[string]any{
		"Title":   "Selatpay Dashboard",
		"Intents": rows,
		"Limit":   limit,
	})
}

func (s *Server) handleIntent(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "bad intent id", http.StatusBadRequest)
		return
	}
	view, err := s.loadIntentView(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "load intent: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, "detail.html", view)
}

// intentView aggregates everything the detail page renders. The
// handler issues separate small queries rather than one wide join
// because the cardinality is bounded (at most a few rows per
// section per intent) and SQL diffs stay readable.
type intentView struct {
	Title     string
	Intent    dbq.PaymentIntent
	Quote     dbq.Quote
	Onchain   []dbq.OnchainPayment
	Entries   []journalEntryView
	Payout    *dbq.Payout
	Saga      *dbq.SagaRun
	Outbox    []dbq.Outbox
	Merchant  dbq.Merchant
}

type journalEntryView struct {
	Entry    dbq.JournalEntry
	Postings []postingView
}

type postingView struct {
	Posting     dbq.Posting
	AccountCode string
}

func (s *Server) loadIntentView(ctx context.Context, id uuid.UUID) (intentView, error) {
	q := dbq.New(s.pool)
	intent, err := q.GetPaymentIntentByID(ctx, ldb.PgUUID(id))
	if err != nil {
		return intentView{}, err
	}
	merchant, err := q.GetMerchantByID(ctx, intent.MerchantID)
	if err != nil {
		return intentView{}, err
	}
	quote, err := q.GetQuote(ctx, intent.QuoteID)
	if err != nil {
		return intentView{}, err
	}
	onchain, err := q.ListOnchainPaymentsByIntent(ctx, ldb.PgUUIDPtr(&id))
	if err != nil {
		return intentView{}, err
	}
	entryRows, err := q.ListJournalEntriesByIntent(ctx, ldb.PgUUIDPtr(&id))
	if err != nil {
		return intentView{}, err
	}
	entries := make([]journalEntryView, 0, len(entryRows))
	for _, e := range entryRows {
		postings, err := q.ListPostingsByEntry(ctx, e.ID)
		if err != nil {
			return intentView{}, err
		}
		pviews := make([]postingView, 0, len(postings))
		for _, p := range postings {
			acct, err := q.GetAccountByID(ctx, p.AccountID)
			if err != nil {
				return intentView{}, err
			}
			pviews = append(pviews, postingView{Posting: p, AccountCode: acct.Code})
		}
		entries = append(entries, journalEntryView{Entry: e, Postings: pviews})
	}

	view := intentView{
		Title:    "Intent " + id.String(),
		Intent:   intent,
		Quote:    quote,
		Onchain:  onchain,
		Entries:  entries,
		Merchant: merchant,
	}

	if po, err := q.GetPayoutByIntent(ctx, ldb.PgUUID(id)); err == nil {
		view.Payout = &po
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return intentView{}, err
	}

	if run, err := q.GetSagaRunByIntent(ctx, dbq.GetSagaRunByIntentParams{
		IntentID: ldb.PgUUID(id),
		SagaKind: "intent_settlement",
	}); err == nil {
		view.Saga = &run
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return intentView{}, err
	}

	if outboxRows, err := q.ListOutboxByAggregate(ctx, ldb.PgUUIDPtr(&id)); err == nil {
		view.Outbox = outboxRows
	} else {
		return intentView{}, err
	}
	return view, nil
}

func (s *Server) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, "render: "+err.Error(), http.StatusInternalServerError)
		return
	}
}

// funcMap exposes a handful of formatting helpers the templates use
// to avoid forcing handlers to massage data into display-ready form.
func funcMap() template.FuncMap {
	return template.FuncMap{
		"fmtTime": func(t time.Time) string {
			if t.IsZero() {
				return ""
			}
			return t.UTC().Format("2006-01-02 15:04:05 UTC")
		},
		"deref": func(p *string) string {
			if p == nil {
				return ""
			}
			return *p
		},
		"shortUUID": func(s string) string {
			if len(s) < 8 {
				return s
			}
			return s[:8]
		},
	}
}
