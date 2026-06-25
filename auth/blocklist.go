package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Blocked is one entry on the platform-admin ban blocklist. A ban (as
// distinct from a reversible suspend) drops a row here so the banned
// identity can never re-register: the signup path consults IsBlocked
// before minting an account.
//
// Value is always lowercased. Kind picks how it matches a candidate
// email:
//
//   - BlockEmail  — exact email match ("alice@acme.test").
//   - BlockDomain — the value is a bare domain ("acme.test") and matches
//     any email at that domain. Stored without the leading "@".
type Blocked struct {
	Value     string    `json:"value"`
	Kind      string    `json:"kind"`
	Reason    string    `json:"reason,omitempty"`
	CreatedBy string    `json:"created_by,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

const (
	BlockEmail  = "email"
	BlockDomain = "domain"
)

// BlocklistStore is the ban-list boundary. A nil store means "nothing is
// blocked" — callers (the signup handler) treat that as an open list, so
// deployments without a Postgres backend simply can't ban (and don't
// crash). Postgres-only, like the membership/profile stores.
type BlocklistStore interface {
	// IsBlocked reports whether email is banned, by exact match or by its
	// domain. email is normalized (lowercased, trimmed) internally.
	IsBlocked(ctx context.Context, email string) (bool, Blocked, error)
	// Block adds (or refreshes) a blocklist entry. Idempotent on value.
	Block(ctx context.Context, b Blocked) error
	// Unblock removes an entry by its (already-normalized) value.
	Unblock(ctx context.Context, value string) error
	// List returns every entry, newest first — the platform-admin view.
	List(ctx context.Context) ([]Blocked, error)
}

// NormalizeBlockEmail lowercases and trims an email for blocklist
// storage and comparison.
func NormalizeBlockEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// emailDomain returns the lowercased domain part of an email, or "" if
// the address has no usable domain.
func emailDomain(email string) string {
	email = NormalizeBlockEmail(email)
	at := strings.LastIndex(email, "@")
	if at < 1 || at == len(email)-1 {
		return ""
	}
	return email[at+1:]
}

const pgBlocklistSchema = `
CREATE TABLE IF NOT EXISTS blocked_identities (
    value      TEXT PRIMARY KEY,
    kind       TEXT NOT NULL,
    reason     TEXT NOT NULL DEFAULT '',
    created_by TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
`

// EnsurePgBlocklistSchema creates the blocked_identities table.
func EnsurePgBlocklistSchema(ctx context.Context, pool *pgxpool.Pool) error {
	if pool == nil {
		return fmt.Errorf("nil pool")
	}
	_, err := pool.Exec(ctx, pgBlocklistSchema)
	return err
}

// PgBlocklistStore is the Postgres BlocklistStore.
type PgBlocklistStore struct {
	pool *pgxpool.Pool
}

func NewPgBlocklistStore(ctx context.Context, pool *pgxpool.Pool) (*PgBlocklistStore, error) {
	if err := EnsurePgBlocklistSchema(ctx, pool); err != nil {
		return nil, err
	}
	return &PgBlocklistStore{pool: pool}, nil
}

func (s *PgBlocklistStore) IsBlocked(ctx context.Context, email string) (bool, Blocked, error) {
	email = NormalizeBlockEmail(email)
	if email == "" {
		return false, Blocked{}, nil
	}
	domain := emailDomain(email)
	// One round-trip: match the exact email OR a domain row equal to the
	// candidate's domain. Exact-email wins when both exist (ORDER BY kind
	// puts 'domain' before 'email' alphabetically, so prefer 'email' last
	// — flip with DESC).
	const q = `
		SELECT value, kind, reason, created_by, created_at
		FROM blocked_identities
		WHERE value = $1 OR (kind = 'domain' AND value = $2)
		ORDER BY kind DESC
		LIMIT 1`
	b, err := scanBlocked(s.pool.QueryRow(ctx, q, email, domain))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, Blocked{}, nil
		}
		return false, Blocked{}, err
	}
	return true, b, nil
}

func (s *PgBlocklistStore) Block(ctx context.Context, b Blocked) error {
	b.Value = NormalizeBlockEmail(b.Value)
	if b.Value == "" {
		return fmt.Errorf("value required")
	}
	if b.Kind == "" {
		b.Kind = BlockEmail
	}
	created := b.CreatedAt
	if created.IsZero() {
		created = time.Now().UTC()
	}
	const q = `
		INSERT INTO blocked_identities (value, kind, reason, created_by, created_at)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (value) DO UPDATE SET
			kind=EXCLUDED.kind, reason=EXCLUDED.reason,
			created_by=EXCLUDED.created_by, created_at=EXCLUDED.created_at`
	_, err := s.pool.Exec(ctx, q, b.Value, b.Kind, b.Reason, b.CreatedBy, created)
	return err
}

func (s *PgBlocklistStore) Unblock(ctx context.Context, value string) error {
	value = NormalizeBlockEmail(value)
	_, err := s.pool.Exec(ctx, `DELETE FROM blocked_identities WHERE value=$1`, value)
	return err
}

func (s *PgBlocklistStore) List(ctx context.Context) ([]Blocked, error) {
	const q = `SELECT value, kind, reason, created_by, created_at
		FROM blocked_identities ORDER BY created_at DESC`
	return queryRows(ctx, s.pool, scanBlocked, q)
}

func scanBlocked(row rowScanner) (Blocked, error) {
	var b Blocked
	if err := row.Scan(&b.Value, &b.Kind, &b.Reason, &b.CreatedBy, &b.CreatedAt); err != nil {
		return Blocked{}, err
	}
	return b, nil
}
