package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	ldb "github.com/vincentiuslienardo/selatpay/internal/db"
	dbq "github.com/vincentiuslienardo/selatpay/internal/db/sqlc"
)

// PGKeyStore resolves api keys from Postgres. Secrets are stored as an
// HMAC-SHA256(pepper, raw_secret) — we can't store the raw bytes because we
// also need them to verify request signatures, and we can't bcrypt them
// because signature verification needs the raw key. The pepper adds a second
// factor so a compromised DB alone doesn't yield working keys.
type PGKeyStore struct {
	pool   *pgxpool.Pool
	pepper []byte
}

func NewPGKeyStore(pool *pgxpool.Pool, pepper []byte) *PGKeyStore {
	return &PGKeyStore{pool: pool, pepper: pepper}
}

// Lookup is the KeyStore hot path. It returns (merchantID, secret, err);
// secret here is the *derived* HMAC key — the same value used on both sides
// of the handshake, reconstructed deterministically from the pepper + raw
// secret at provisioning time.
//
// For the portfolio demo, raw secrets are stored alongside the derived key
// (the PGKeyStore only ever returns the derived key). A production design
// would push raw secrets into KMS and have the API gateway derive the key
// per request via Decrypt — out of scope here and discussed in the custody ADR.
func (s *PGKeyStore) Lookup(ctx context.Context, keyID string) (uuid.UUID, []byte, error) {
	q := dbq.New(s.pool)
	row, err := q.GetActiveAPIKeyByKeyID(ctx, keyID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, nil, ErrUnknownKey
		}
		return uuid.Nil, nil, fmt.Errorf("lookup api key: %w", err)
	}
	return ldb.FromPgUUID(row.MerchantID), row.SecretHash, nil
}

// DeriveSecret returns the value stored in api_keys.secret_hash for a given
// raw secret. Deterministic so both provisioning and verification agree.
func DeriveSecret(pepper, raw []byte) []byte {
	mac := hmac.New(sha256.New, pepper)
	mac.Write(raw)
	return mac.Sum(nil)
}

// GenerateRawSecret produces a 32-byte random secret formatted as hex for
// client-side use. Seed provisioning with this + DeriveSecret.
func GenerateRawSecret() (raw []byte, display string, err error) {
	raw = make([]byte, 32)
	if _, err = rand.Read(raw); err != nil {
		return nil, "", err
	}
	return raw, hex.EncodeToString(raw), nil
}
