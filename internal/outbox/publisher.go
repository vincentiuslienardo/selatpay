package outbox

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	ldb "github.com/vincentiuslienardo/selatpay/internal/db"
	dbq "github.com/vincentiuslienardo/selatpay/internal/db/sqlc"
)

// Publish writes a message to the outbox inside the caller's transaction.
// Producers — saga steps, in particular — call this so the message and
// the business state change commit atomically. If the surrounding tx
// rolls back, the outbox row is rolled back with it; if it commits, the
// row is durably visible to the dispatcher on its next claim.
//
// Headers may be nil. Payload may be nil for fire-and-forget signals,
// but JSON null is preferred over zero-length bytes to keep the column
// strictly valid JSON.
func Publish(
	ctx context.Context,
	tx pgx.Tx,
	topic string,
	aggregateID *uuid.UUID,
	payload json.RawMessage,
	headers map[string]string,
) (Message, error) {
	if topic == "" {
		return Message{}, fmt.Errorf("outbox: topic is required")
	}
	if len(payload) == 0 {
		payload = json.RawMessage("null")
	}

	headersJSON, err := json.Marshal(headersOrEmpty(headers))
	if err != nil {
		return Message{}, fmt.Errorf("outbox: marshal headers: %w", err)
	}

	q := dbq.New(tx)
	row, err := q.PublishOutbox(ctx, dbq.PublishOutboxParams{
		Topic:       topic,
		AggregateID: ldb.PgUUIDPtr(aggregateID),
		Payload:     payload,
		Headers:     headersJSON,
	})
	if err != nil {
		return Message{}, fmt.Errorf("outbox: insert: %w", err)
	}
	return toMessage(row)
}

func headersOrEmpty(h map[string]string) map[string]string {
	if h == nil {
		return map[string]string{}
	}
	return h
}

func toMessage(row dbq.Outbox) (Message, error) {
	msg := Message{
		ID:          ldb.FromPgUUID(row.ID),
		Topic:       row.Topic,
		AggregateID: ldb.FromPgUUIDPtr(row.AggregateID),
		Attempts:    row.Attempts,
		CreatedAt:   row.CreatedAt.Time,
		Payload:     row.Payload,
	}
	if len(row.Headers) > 0 {
		var hdrs map[string]string
		if err := json.Unmarshal(row.Headers, &hdrs); err != nil {
			return Message{}, fmt.Errorf("outbox: unmarshal headers: %w", err)
		}
		msg.Headers = hdrs
	}
	return msg, nil
}
