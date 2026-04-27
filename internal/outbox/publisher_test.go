package outbox

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	dbq "github.com/vincentiuslienardo/selatpay/internal/db/sqlc"
)

func TestToMessage_DecodesHeadersAndAggregateID(t *testing.T) {
	id := uuid.New()
	agg := uuid.New()
	row := dbq.Outbox{
		ID:          pgtype.UUID{Bytes: id, Valid: true},
		Topic:       "intent.completed",
		AggregateID: pgtype.UUID{Bytes: agg, Valid: true},
		Payload:     []byte(`{"foo":"bar"}`),
		Headers:     []byte(`{"X-Idempotency":"abc"}`),
		Attempts:    1,
	}
	msg, err := toMessage(row)
	if err != nil {
		t.Fatalf("toMessage: %v", err)
	}
	if msg.ID != id {
		t.Errorf("ID mismatch: %s vs %s", msg.ID, id)
	}
	if msg.AggregateID == nil || *msg.AggregateID != agg {
		t.Errorf("AggregateID mismatch: %v vs %v", msg.AggregateID, agg)
	}
	if msg.Headers["X-Idempotency"] != "abc" {
		t.Errorf("header decode: %v", msg.Headers)
	}
}

func TestToMessage_NilAggregateID(t *testing.T) {
	row := dbq.Outbox{
		ID:          pgtype.UUID{Bytes: uuid.New(), Valid: true},
		Topic:       "t",
		AggregateID: pgtype.UUID{},
		Payload:     []byte("null"),
		Headers:     []byte("{}"),
	}
	msg, err := toMessage(row)
	if err != nil {
		t.Fatalf("toMessage: %v", err)
	}
	if msg.AggregateID != nil {
		t.Errorf("expected nil AggregateID, got %v", msg.AggregateID)
	}
}

func TestHeadersOrEmpty_ReplacesNil(t *testing.T) {
	got := headersOrEmpty(nil)
	if got == nil {
		t.Fatal("expected non-nil map")
	}
	if len(got) != 0 {
		t.Fatalf("expected empty map, got %v", got)
	}
}

func TestPublisherJSON_RoundTrips(t *testing.T) {
	headers := map[string]string{"X-Topic": "intent.completed"}
	bs, err := json.Marshal(headers)
	if err != nil {
		t.Fatalf("marshal headers: %v", err)
	}
	var back map[string]string
	if err := json.Unmarshal(bs, &back); err != nil {
		t.Fatalf("unmarshal headers: %v", err)
	}
	if back["X-Topic"] != "intent.completed" {
		t.Errorf("round-trip mismatch: %v", back)
	}
}
