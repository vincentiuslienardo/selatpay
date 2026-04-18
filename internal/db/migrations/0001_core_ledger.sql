-- +goose Up
-- +goose StatementBegin
CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TYPE account_type AS ENUM ('asset', 'liability', 'equity', 'revenue', 'expense');
CREATE TYPE posting_direction AS ENUM ('debit', 'credit');

CREATE TABLE accounts (
    id          UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    code        TEXT         NOT NULL,
    type        account_type NOT NULL,
    currency    TEXT         NOT NULL,
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    CONSTRAINT accounts_code_currency_key UNIQUE (code, currency)
);
CREATE INDEX accounts_code_idx ON accounts (code);

CREATE TABLE journal_entries (
    id            UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    external_ref  TEXT         NOT NULL,
    kind          TEXT         NOT NULL,
    description   TEXT,
    intent_id     UUID,
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    CONSTRAINT journal_entries_external_ref_key UNIQUE (external_ref)
);
CREATE INDEX journal_entries_intent_idx ON journal_entries (intent_id) WHERE intent_id IS NOT NULL;
CREATE INDEX journal_entries_kind_idx   ON journal_entries (kind);

CREATE TABLE postings (
    id                UUID               PRIMARY KEY DEFAULT gen_random_uuid(),
    journal_entry_id  UUID               NOT NULL REFERENCES journal_entries(id) ON DELETE RESTRICT,
    account_id        UUID               NOT NULL REFERENCES accounts(id)        ON DELETE RESTRICT,
    amount            BIGINT             NOT NULL CHECK (amount > 0),
    currency          TEXT               NOT NULL,
    direction         posting_direction  NOT NULL,
    created_at        TIMESTAMPTZ        NOT NULL DEFAULT NOW()
);
CREATE INDEX postings_entry_idx   ON postings (journal_entry_id);
CREATE INDEX postings_account_idx ON postings (account_id);
-- +goose StatementEnd

-- +goose StatementBegin
-- Every posting's currency must match its account's currency. Cross-currency
-- transfers are represented as two journal entries, not mixed postings inside
-- one entry, so this invariant can be checked row-local.
CREATE OR REPLACE FUNCTION ledger_check_posting_currency() RETURNS trigger AS $$
DECLARE
    acct_currency TEXT;
BEGIN
    SELECT currency INTO acct_currency FROM accounts WHERE id = NEW.account_id;
    IF acct_currency IS NULL THEN
        RAISE EXCEPTION 'posting references unknown account %', NEW.account_id;
    END IF;
    IF NEW.currency <> acct_currency THEN
        RAISE EXCEPTION 'posting currency % does not match account currency %', NEW.currency, acct_currency;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER postings_currency_matches_account
    BEFORE INSERT ON postings
    FOR EACH ROW EXECUTE FUNCTION ledger_check_posting_currency();
-- +goose StatementEnd

-- +goose StatementBegin
-- Balanced-entry invariant: at transaction commit, every journal_entry must have
-- SUM(debit) = SUM(credit) per currency. Implemented as a DEFERRABLE constraint
-- trigger so mid-transaction intermediate states (inserting postings one at a
-- time) don't trip the check; only the final committed state must balance.
CREATE OR REPLACE FUNCTION ledger_check_entry_balanced() RETURNS trigger AS $$
DECLARE
    diff_row RECORD;
BEGIN
    FOR diff_row IN
        SELECT currency,
               SUM(CASE direction WHEN 'debit' THEN amount ELSE -amount END) AS diff
        FROM postings
        WHERE journal_entry_id = NEW.journal_entry_id
        GROUP BY currency
        HAVING SUM(CASE direction WHEN 'debit' THEN amount ELSE -amount END) <> 0
    LOOP
        RAISE EXCEPTION 'journal entry % unbalanced in currency %: debit-credit = %',
            NEW.journal_entry_id, diff_row.currency, diff_row.diff
            USING ERRCODE = '23514';  -- check_violation
    END LOOP;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE CONSTRAINT TRIGGER postings_entry_balanced
    AFTER INSERT ON postings
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION ledger_check_entry_balanced();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS postings_entry_balanced        ON postings;
DROP TRIGGER IF EXISTS postings_currency_matches_account ON postings;
DROP FUNCTION IF EXISTS ledger_check_entry_balanced();
DROP FUNCTION IF EXISTS ledger_check_posting_currency();
DROP TABLE IF EXISTS postings;
DROP TABLE IF EXISTS journal_entries;
DROP TABLE IF EXISTS accounts;
DROP TYPE IF EXISTS posting_direction;
DROP TYPE IF EXISTS account_type;
-- +goose StatementEnd
