package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"git.sr.ht/~klahr/hazy-flow/core"
)

// SessionTokenPrefix marks credentials as session tokens (vs API keys,
// which use apiKeyPrefix). Cookies and bearer headers carry the prefix
// + opaque secret format so a single Authenticate path covers both.
const SessionTokenPrefix = "hzs_"

// SessionLookupKey maps a session token to the key its record is stored
// under. Sessions are stored keyed by the SHA-256 of the token, never the
// token itself — so a leak of the session store (a DB backup, a read
// replica, a SQLi elsewhere) hands out hashes, not live bearer
// credentials, mirroring how API-key secrets are stored hashed. The token
// is 256 bits of CSPRNG output, so an unsalted hash suffices: salt defends
// low-entropy secrets like passwords against precomputation, and adds
// nothing for a high-entropy random token.
//
// Callers that look a session up by its raw token (sign-out, org switch)
// must pass the token through this first.
func SessionLookupKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Session is the server-side record. The principal lives here so
// revocation is immediate and the cookie cannot be spoofed offline. ID is
// the SHA-256 of the token (the store key), not the token itself — see
// SessionLookupKey.
type Session struct {
	ID        string
	Subject   string
	Tenant    string
	Workspace string
	Roles     []core.Role
	CreatedAt time.Time
	ExpiresAt time.Time
}

// SessionStore is the session lookup boundary used by both the
// authenticator (read) and the signin/signout handlers (write).
type SessionStore interface {
	GetSession(ctx context.Context, id string) (Session, error)
	PutSession(ctx context.Context, s Session) error
	DeleteSession(ctx context.Context, id string) error
}

// MemSessionStore keeps sessions in process memory. They die on daemon
// restart — users re-sign-in, which matches the dev-deployment
// expectation (and matches the API-key keystore's behavior).
type MemSessionStore struct {
	mu       sync.RWMutex
	sessions map[string]Session
}

func NewMemSessionStore() *MemSessionStore {
	return &MemSessionStore{sessions: make(map[string]Session)}
}

func (s *MemSessionStore) GetSession(_ context.Context, id string) (Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.sessions[id]
	if !ok {
		return Session{}, ErrInvalidCredential
	}
	return sess, nil
}

func (s *MemSessionStore) PutSession(_ context.Context, sess Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[sess.ID] = sess
	return nil
}

func (s *MemSessionStore) DeleteSession(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, id)
	return nil
}

// SessionAuthenticator slots into the auth Chain alongside
// APIKeyAuthenticator. It recognizes tokens with SessionTokenPrefix;
// anything else falls through to the next provider.
type SessionAuthenticator struct {
	Store SessionStore
	Clock func() time.Time
}

func (a *SessionAuthenticator) Authenticate(ctx context.Context, credential string) (core.Principal, error) {
	if !strings.HasPrefix(credential, SessionTokenPrefix) {
		return core.Principal{}, ErrInvalidCredential
	}
	// The record is keyed by the token's hash, never the token itself.
	key := SessionLookupKey(credential)
	sess, err := a.Store.GetSession(ctx, key)
	if err != nil {
		return core.Principal{}, ErrInvalidCredential
	}
	if a.now().After(sess.ExpiresAt) {
		_ = a.Store.DeleteSession(ctx, key)
		return core.Principal{}, fmt.Errorf("%w: session expired", ErrInvalidCredential)
	}
	return core.Principal{
		Subject:   sess.Subject,
		Tenant:    sess.Tenant,
		Workspace: sess.Workspace,
		Roles:     sess.Roles,
	}, nil
}

func (a *SessionAuthenticator) now() time.Time {
	if a.Clock != nil {
		return a.Clock()
	}
	return time.Now()
}

// IssueSession persists a fresh session for the given user and returns
// the record plus the opaque token. The token doubles as cookie value and
// bearer credential; only its hash is persisted (sess.ID), so the stored
// record can't be replayed as a credential if the store leaks.
func IssueSession(ctx context.Context, store SessionStore, user User, ttl time.Duration) (Session, string, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return Session{}, "", fmt.Errorf("session secret: %w", err)
	}
	token := SessionTokenPrefix + hex.EncodeToString(secret)
	now := time.Now()
	sess := Session{
		// Store the token's hash, not the token — see SessionLookupKey.
		ID:        SessionLookupKey(token),
		Subject:   user.Subject,
		Tenant:    user.Tenant,
		Workspace: user.Workspace,
		Roles:     user.Roles,
		CreatedAt: now,
		ExpiresAt: now.Add(ttl),
	}
	if err := store.PutSession(ctx, sess); err != nil {
		return Session{}, "", err
	}
	return sess, token, nil
}
