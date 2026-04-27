package outbox

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Message is the in-memory shape of an outbox row. Payload stays as a
// JSON RawMessage so the publisher can hand off whatever the producer
// already serialized without a round-trip through interface{}.
type Message struct {
	ID          uuid.UUID
	Topic       string
	AggregateID *uuid.UUID
	Payload     json.RawMessage
	Headers     map[string]string
	Attempts    int32
	CreatedAt   time.Time
}

// Sender abstracts the destination of dispatched messages: webhook
// HTTP, queue producer, or test stub. The dispatcher only needs Send
// to be idempotent enough that a duplicate delivery (e.g., crash
// after Send before MarkOutboxDelivered) is harmless on the receiver
// side. For webhooks that's typically achieved with the message ID
// included as an idempotency key in the headers.
type Sender interface {
	Send(ctx context.Context, msg Message) error
}

// SenderFunc adapts an ordinary function to the Sender interface.
type SenderFunc func(ctx context.Context, msg Message) error

func (f SenderFunc) Send(ctx context.Context, msg Message) error { return f(ctx, msg) }
