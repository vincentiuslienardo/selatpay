package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	oapitypes "github.com/oapi-codegen/runtime/types"

	"github.com/vincentiuslienardo/selatpay/internal/api/apispec"
	"github.com/vincentiuslienardo/selatpay/internal/auth"
	ldb "github.com/vincentiuslienardo/selatpay/internal/db"
	dbq "github.com/vincentiuslienardo/selatpay/internal/db/sqlc"
	"github.com/vincentiuslienardo/selatpay/internal/quoter"
)

// Handlers satisfies the oapi-codegen-generated ServerInterface.
type Handlers struct {
	Pool   *pgxpool.Pool
	Quoter *quoter.Quoter
}

func (h *Handlers) Healthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (h *Handlers) CreatePaymentIntent(w http.ResponseWriter, r *http.Request, _ apispec.CreatePaymentIntentParams) {
	merchantID, ok := auth.MerchantFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "no merchant on context")
		return
	}

	var body apispec.CreatePaymentIntentRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if err := validateCreateRequest(body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	q := dbq.New(h.Pool)
	if existing, err := q.GetPaymentIntentByMerchantRef(r.Context(), dbq.GetPaymentIntentByMerchantRefParams{
		MerchantID:  ldb.PgUUID(merchantID),
		ExternalRef: body.ExternalRef,
	}); err == nil {
		quote, err := q.GetQuote(r.Context(), existing.QuoteID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "fetch_quote", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, intentToAPI(existing, quote))
		return
	} else if !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "lookup", err.Error())
		return
	}

	qt, err := h.Quoter.Issue(r.Context(), quoter.PairUSDCIDR, body.AmountIdr)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "quote_failed", err.Error())
		return
	}

	created, err := q.CreatePaymentIntent(r.Context(), dbq.CreatePaymentIntentParams{
		MerchantID:       ldb.PgUUID(merchantID),
		ExternalRef:      body.ExternalRef,
		AmountIdr:        body.AmountIdr,
		QuotedAmountUsdc: qt.AmountUSDC,
		QuoteID:          ldb.PgUUID(qt.ID),
		State:            dbq.PaymentIntentStatePending,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "create_intent", err.Error())
		return
	}

	dbQuote, err := q.GetQuote(r.Context(), created.QuoteID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "fetch_quote", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, intentToAPI(created, dbQuote))
}

func (h *Handlers) GetPaymentIntent(w http.ResponseWriter, r *http.Request, id oapitypes.UUID) {
	merchantID, ok := auth.MerchantFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "no merchant on context")
		return
	}

	q := dbq.New(h.Pool)
	intent, err := q.GetPaymentIntentByID(r.Context(), ldb.PgUUID(uuid.UUID(id)))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "not_found", "payment intent not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "fetch_intent", err.Error())
		return
	}
	if ldb.FromPgUUID(intent.MerchantID) != merchantID {
		// Never leak existence across tenants.
		writeError(w, http.StatusNotFound, "not_found", "payment intent not found")
		return
	}
	quote, err := q.GetQuote(r.Context(), intent.QuoteID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "fetch_quote", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, intentToAPI(intent, quote))
}

func validateCreateRequest(r apispec.CreatePaymentIntentRequest) error {
	if strings.TrimSpace(r.ExternalRef) == "" {
		return errors.New("external_ref is required")
	}
	if len(r.ExternalRef) > 128 {
		return errors.New("external_ref exceeds 128 characters")
	}
	if r.AmountIdr <= 0 {
		return errors.New("amount_idr must be positive")
	}
	return nil
}

func intentToAPI(p dbq.PaymentIntent, q dbq.Quote) apispec.PaymentIntent {
	return apispec.PaymentIntent{
		Id:               oapitypes.UUID(ldb.FromPgUUID(p.ID)),
		MerchantId:       oapitypes.UUID(ldb.FromPgUUID(p.MerchantID)),
		ExternalRef:      p.ExternalRef,
		AmountIdr:        p.AmountIdr,
		QuotedAmountUsdc: p.QuotedAmountUsdc,
		State:            apispec.PaymentIntentState(p.State),
		ReferencePubkey:  p.ReferencePubkey,
		CreatedAt:        p.CreatedAt.Time,
		Quote: apispec.Quote{
			Id:        oapitypes.UUID(ldb.FromPgUUID(q.ID)),
			Pair:      q.Pair,
			Rate:      formatRate(q.RateNum, q.RateScale),
			SpreadBps: q.SpreadBps,
			ExpiresAt: q.ExpiresAt.Time,
		},
	}
}

// formatRate renders rate_num / 10^rate_scale as a non-scientific decimal
// string so clients see e.g. "16200" or "0.0000617", never "1.62e+04".
func formatRate(num int64, scale int16) string {
	if scale <= 0 {
		return fmt.Sprintf("%d", num)
	}
	str := fmt.Sprintf("%d", num)
	if int(scale) >= len(str) {
		pad := int(scale) - len(str)
		return "0." + strings.Repeat("0", pad) + str
	}
	return str[:len(str)-int(scale)] + "." + str[len(str)-int(scale):]
}

