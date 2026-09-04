// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Short-lived auth state that spans two requests.
//
// Four flows mint a token on one request and consume it on the next, moments
// later: the Google sign-in state, the connector OAuth pending authorization,
// the per-org-subdomain sign-in handoff, and the TOTP challenge. Each used to
// live in the minting process's memory, which is why multi-replica deployments
// needed sticky sessions — the second request landing on another pod found
// nothing there and the user saw "invalid or expired state" at random.
//
// They differ only in what they carry, so one store backs all of them, keyed by
// a `kind` that keeps their namespaces apart.

// ErrEphemeralNotFound means the token is unknown, already consumed, or past
// its expiry — which callers treat identically, since none of them may reveal
// which.
var ErrEphemeralNotFound = errors.New("ephemeral auth state not found")

// EphemeralStore holds short-lived, single-use auth state.
//
// Expiry is the store's business: a Get past expires_at reports
// ErrEphemeralNotFound whether or not the row has been swept yet, so no caller
// can accept a stale token because a sweep was late.
type EphemeralStore interface {
	Put(ctx context.Context, kind, token string, payload []byte, expiresAt time.Time) error
	// Get returns the payload and the attempt count.
	Get(ctx context.Context, kind, token string) ([]byte, int, error)
	Delete(ctx context.Context, kind, token string) error
	// IncrAttempts atomically increments and returns the failed-guess count.
	// Atomic because concurrent wrong guesses against one token must not all
	// read the same count and lose increments, which would weaken a
	// per-challenge brute-force cap.
	IncrAttempts(ctx context.Context, kind, token string) (int, error)
	// Sweep removes expired rows and reports how many went.
	Sweep(ctx context.Context) (int, error)
}

// Ephemeral kinds. Distinct namespaces so a token minted for one flow can never
// be redeemed by another.
const (
	EphemeralGoogleSignIn  = "google_signin"
	EphemeralOAuthPending  = "oauth_pending"
	EphemeralSignInHandoff = "signin_handoff"
	EphemeralTOTPChallenge = "totp_challenge"
)

type ephemeralEntry struct {
	payload   []byte
	attempts  int
	expiresAt time.Time
}

// MemEphemeralStore keeps state in process memory — the single-node default,
// and what tests use. Expired entries are swept lazily on access so abandoned
// flows don't accumulate.
type MemEphemeralStore struct {
	mu    sync.Mutex
	items map[string]ephemeralEntry
}

func NewMemEphemeralStore() *MemEphemeralStore {
	return &MemEphemeralStore{items: map[string]ephemeralEntry{}}
}

func ephemeralKey(kind, token string) string { return kind + "\x00" + token }

func (s *MemEphemeralStore) Put(_ context.Context, kind, token string, payload []byte, expiresAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(time.Now())
	s.items[ephemeralKey(kind, token)] = ephemeralEntry{
		payload:   append([]byte(nil), payload...),
		expiresAt: expiresAt,
	}
	return nil
}

func (s *MemEphemeralStore) Get(_ context.Context, kind, token string) ([]byte, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.items[ephemeralKey(kind, token)]
	if !ok || !time.Now().Before(e.expiresAt) {
		return nil, 0, ErrEphemeralNotFound
	}
	return append([]byte(nil), e.payload...), e.attempts, nil
}

func (s *MemEphemeralStore) Delete(_ context.Context, kind, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.items, ephemeralKey(kind, token))
	return nil
}

func (s *MemEphemeralStore) IncrAttempts(_ context.Context, kind, token string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := ephemeralKey(kind, token)
	e, ok := s.items[k]
	if !ok || !time.Now().Before(e.expiresAt) {
		return 0, ErrEphemeralNotFound
	}
	e.attempts++
	s.items[k] = e
	return e.attempts, nil
}

func (s *MemEphemeralStore) Sweep(context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sweepLocked(time.Now()), nil
}

func (s *MemEphemeralStore) sweepLocked(now time.Time) int {
	n := 0
	for k, e := range s.items {
		if !now.Before(e.expiresAt) {
			delete(s.items, k)
			n++
		}
	}
	return n
}

// PgEphemeralStore is the durable EphemeralStore: state minted on one replica
// is redeemable on any other, which is what removes the sticky-session
// requirement from a multi-replica deployment.
type PgEphemeralStore struct {
	pool *pgxpool.Pool
}

func NewPgEphemeralStore(ctx context.Context, pool *pgxpool.Pool) (*PgEphemeralStore, error) {
	if err := EnsurePgAuthSchema(ctx, pool); err != nil {
		return nil, err
	}
	return &PgEphemeralStore{pool: pool}, nil
}

func (s *PgEphemeralStore) Put(ctx context.Context, kind, token string, payload []byte, expiresAt time.Time) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO auth_ephemeral (kind, token, payload, expires_at) VALUES ($1,$2,$3,$4)
		 ON CONFLICT (kind, token) DO UPDATE
		   SET payload=EXCLUDED.payload, expires_at=EXCLUDED.expires_at, attempts=0`,
		kind, token, payload, expiresAt.UTC())
	return err
}

func (s *PgEphemeralStore) Get(ctx context.Context, kind, token string) ([]byte, int, error) {
	var (
		payload  []byte
		attempts int
	)
	// The expiry is part of the lookup, not a later check: a row the sweep has
	// not reached yet must still read as gone.
	err := s.pool.QueryRow(ctx,
		`SELECT payload, attempts FROM auth_ephemeral
		  WHERE kind=$1 AND token=$2 AND expires_at > now()`, kind, token).Scan(&payload, &attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, 0, ErrEphemeralNotFound
	}
	if err != nil {
		return nil, 0, err
	}
	return payload, attempts, nil
}

func (s *PgEphemeralStore) Delete(ctx context.Context, kind, token string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM auth_ephemeral WHERE kind=$1 AND token=$2`, kind, token)
	return err
}

func (s *PgEphemeralStore) IncrAttempts(ctx context.Context, kind, token string) (int, error) {
	var attempts int
	err := s.pool.QueryRow(ctx,
		`UPDATE auth_ephemeral SET attempts = attempts + 1
		  WHERE kind=$1 AND token=$2 AND expires_at > now()
		  RETURNING attempts`, kind, token).Scan(&attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrEphemeralNotFound
	}
	return attempts, err
}

func (s *PgEphemeralStore) Sweep(ctx context.Context) (int, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM auth_ephemeral WHERE expires_at <= now()`)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// EphemeralTOTPChallengeStore adapts an EphemeralStore to the challenge
// interface, so a Postgres-backed deployment carries a half-finished 2FA
// sign-in across replicas instead of losing it when the second request lands
// elsewhere.
type EphemeralTOTPChallengeStore struct {
	store EphemeralStore
}

func NewEphemeralTOTPChallengeStore(store EphemeralStore) *EphemeralTOTPChallengeStore {
	return &EphemeralTOTPChallengeStore{store: store}
}

func (s *EphemeralTOTPChallengeStore) Put(ctx context.Context, token string, c TOTPChallenge) error {
	payload, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return s.store.Put(ctx, EphemeralTOTPChallenge, token, payload, c.ExpiresAt)
}

func (s *EphemeralTOTPChallengeStore) Get(ctx context.Context, token string) (TOTPChallenge, error) {
	payload, attempts, err := s.store.Get(ctx, EphemeralTOTPChallenge, token)
	if errors.Is(err, ErrEphemeralNotFound) {
		return TOTPChallenge{}, ErrChallengeUnknown
	}
	if err != nil {
		return TOTPChallenge{}, err
	}
	var c TOTPChallenge
	if err := json.Unmarshal(payload, &c); err != nil {
		return TOTPChallenge{}, err
	}
	// The count lives in the store's own column so increments stay atomic; the
	// copy inside the payload is whatever it was when the challenge was minted.
	c.Attempts = attempts
	return c, nil
}

func (s *EphemeralTOTPChallengeStore) Delete(ctx context.Context, token string) error {
	return s.store.Delete(ctx, EphemeralTOTPChallenge, token)
}

func (s *EphemeralTOTPChallengeStore) IncrAttempts(ctx context.Context, token string) (int, error) {
	n, err := s.store.IncrAttempts(ctx, EphemeralTOTPChallenge, token)
	if errors.Is(err, ErrEphemeralNotFound) {
		return 0, ErrChallengeUnknown
	}
	return n, err
}
