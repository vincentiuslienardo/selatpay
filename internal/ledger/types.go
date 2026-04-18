package ledger

import (
	"time"

	"github.com/google/uuid"
)

type AccountType string

const (
	AccountAsset     AccountType = "asset"
	AccountLiability AccountType = "liability"
	AccountEquity    AccountType = "equity"
	AccountRevenue   AccountType = "revenue"
	AccountExpense   AccountType = "expense"
)

type Direction string

const (
	Debit  Direction = "debit"
	Credit Direction = "credit"
)

type Account struct {
	ID        uuid.UUID
	Code      string
	Type      AccountType
	Currency  string
	CreatedAt time.Time
}

type Entry struct {
	ExternalRef string
	Kind        string
	Description string
	IntentID    *uuid.UUID
	Lines       []Line
}

type Line struct {
	AccountID uuid.UUID
	Amount    int64
	Currency  string
	Direction Direction
}

type JournalEntry struct {
	ID          uuid.UUID
	ExternalRef string
	Kind        string
	Description string
	IntentID    *uuid.UUID
	CreatedAt   time.Time
}

type Posting struct {
	ID             uuid.UUID
	JournalEntryID uuid.UUID
	AccountID      uuid.UUID
	Amount         int64
	Currency       string
	Direction      Direction
	CreatedAt      time.Time
}

type Balance struct {
	AccountID uuid.UUID
	Code      string
	Type      AccountType
	Currency  string
	Amount    int64
}
