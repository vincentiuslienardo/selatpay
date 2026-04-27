package ledger

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	ldb "github.com/vincentiuslienardo/selatpay/internal/db"
	dbq "github.com/vincentiuslienardo/selatpay/internal/db/sqlc"
)

type Ledger struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Ledger {
	return &Ledger{pool: pool}
}

// Post records a balanced journal entry atomically. Idempotent by ExternalRef:
// if an entry with the same external_ref already exists, the existing entry is
// returned and no postings are inserted.
func (l *Ledger) Post(ctx context.Context, e Entry) (JournalEntry, error) {
	if err := Validate(e); err != nil {
		return JournalEntry{}, err
	}

	tx, err := l.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return JournalEntry{}, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	je, err := postWith(ctx, dbq.New(tx), e)
	if err != nil {
		return JournalEntry{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return JournalEntry{}, fmt.Errorf("commit: %w", err)
	}
	return je, nil
}

// PostTx records a balanced journal entry inside the caller's existing
// transaction. Saga steps use this so the journal write commits atomically
// with the saga advance and any other side effects (intent state change,
// outbox publish) the step performs in the same tx.
func (l *Ledger) PostTx(ctx context.Context, tx pgx.Tx, e Entry) (JournalEntry, error) {
	if err := Validate(e); err != nil {
		return JournalEntry{}, err
	}
	return postWith(ctx, dbq.New(tx), e)
}

// postWith runs the validate-already-done lookup-then-insert path against
// any DBTX-backed queries handle. It does not begin or commit the tx; the
// caller controls the transaction lifecycle.
func postWith(ctx context.Context, q *dbq.Queries, e Entry) (JournalEntry, error) {
	if existing, err := q.GetJournalEntryByExternalRef(ctx, e.ExternalRef); err == nil {
		return toJournalEntry(existing), nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return JournalEntry{}, fmt.Errorf("lookup external_ref: %w", err)
	}

	var desc *string
	if e.Description != "" {
		d := e.Description
		desc = &d
	}

	created, err := q.CreateJournalEntry(ctx, dbq.CreateJournalEntryParams{
		ExternalRef: e.ExternalRef,
		Kind:        e.Kind,
		Description: desc,
		IntentID:    ldb.PgUUIDPtr(e.IntentID),
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return JournalEntry{}, ErrDuplicateExternalRef
		}
		return JournalEntry{}, fmt.Errorf("create journal entry: %w", err)
	}

	for _, line := range e.Lines {
		if _, err := q.InsertPosting(ctx, dbq.InsertPostingParams{
			JournalEntryID: created.ID,
			AccountID:      ldb.PgUUID(line.AccountID),
			Amount:         line.Amount,
			Currency:       line.Currency,
			Direction:      dbq.PostingDirection(line.Direction),
		}); err != nil {
			return JournalEntry{}, fmt.Errorf("insert posting: %w", err)
		}
	}

	return toJournalEntry(created), nil
}

// GetAccountByCodeTx resolves a system account inside an existing
// transaction. Returns ErrAccountNotFound if the (code, currency) pair
// does not exist; saga steps treat that as a deploy bug because the
// chart of accounts migration is supposed to seed every code that
// step code references.
func (l *Ledger) GetAccountByCodeTx(ctx context.Context, tx pgx.Tx, code, currency string) (Account, error) {
	q := dbq.New(tx)
	acct, err := q.GetAccountByCodeCurrency(ctx, dbq.GetAccountByCodeCurrencyParams{
		Code:     code,
		Currency: currency,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Account{}, ErrAccountNotFound
		}
		return Account{}, fmt.Errorf("get account by code: %w", err)
	}
	return toAccount(acct), nil
}

func (l *Ledger) CreateAccount(ctx context.Context, code string, accType AccountType, currency string) (Account, error) {
	q := dbq.New(l.pool)
	acct, err := q.CreateAccount(ctx, dbq.CreateAccountParams{
		Code:     code,
		Type:     dbq.AccountType(accType),
		Currency: currency,
	})
	if err != nil {
		return Account{}, fmt.Errorf("create account: %w", err)
	}
	return toAccount(acct), nil
}

func (l *Ledger) GetAccount(ctx context.Context, id uuid.UUID) (Account, error) {
	q := dbq.New(l.pool)
	acct, err := q.GetAccountByID(ctx, ldb.PgUUID(id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Account{}, ErrAccountNotFound
		}
		return Account{}, fmt.Errorf("get account: %w", err)
	}
	return toAccount(acct), nil
}

func (l *Ledger) BalanceOf(ctx context.Context, accountID uuid.UUID) (Balance, error) {
	q := dbq.New(l.pool)
	row, err := q.AccountBalance(ctx, ldb.PgUUID(accountID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Balance{}, ErrAccountNotFound
		}
		return Balance{}, fmt.Errorf("balance: %w", err)
	}
	return Balance{
		AccountID: ldb.FromPgUUID(row.ID),
		Code:      row.Code,
		Type:      AccountType(row.Type),
		Currency:  row.Currency,
		Amount:    row.Balance,
	}, nil
}

func (l *Ledger) ListPostings(ctx context.Context, entryID uuid.UUID) ([]Posting, error) {
	q := dbq.New(l.pool)
	rows, err := q.ListPostingsByEntry(ctx, ldb.PgUUID(entryID))
	if err != nil {
		return nil, fmt.Errorf("list postings: %w", err)
	}
	out := make([]Posting, 0, len(rows))
	for _, r := range rows {
		out = append(out, Posting{
			ID:             ldb.FromPgUUID(r.ID),
			JournalEntryID: ldb.FromPgUUID(r.JournalEntryID),
			AccountID:      ldb.FromPgUUID(r.AccountID),
			Amount:         r.Amount,
			Currency:       r.Currency,
			Direction:      Direction(r.Direction),
			CreatedAt:      r.CreatedAt.Time,
		})
	}
	return out, nil
}

func toJournalEntry(j dbq.JournalEntry) JournalEntry {
	desc := ""
	if j.Description != nil {
		desc = *j.Description
	}
	return JournalEntry{
		ID:          ldb.FromPgUUID(j.ID),
		ExternalRef: j.ExternalRef,
		Kind:        j.Kind,
		Description: desc,
		IntentID:    ldb.FromPgUUIDPtr(j.IntentID),
		CreatedAt:   j.CreatedAt.Time,
	}
}

func toAccount(a dbq.Account) Account {
	return Account{
		ID:        ldb.FromPgUUID(a.ID),
		Code:      a.Code,
		Type:      AccountType(a.Type),
		Currency:  a.Currency,
		CreatedAt: a.CreatedAt.Time,
	}
}
