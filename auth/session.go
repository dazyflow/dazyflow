// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

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

	"git.sr.ht/~klahr/dazyflow/core"
)

// SessionTokenPrefix marks credentials as session tokens (vs API keys,
// which use apiKeyPrefix). Cookies and bearer headers carry the prefix
// + opaque secret format so a single Authenticate path covers both.
const SessionTokenPrefix = "dzs_"

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

// SessionRevoker is an optional SessionStore extension: revoke every
// session a subject holds, forcing re-sign-in. Used when an admin
// changes or removes a member's roles — without it the member's live
// sessions keep the old roles until they happen to sign in again.
type SessionRevoker interface {
	RevokeSubjectSessions(ctx context.Context, subject string) (int, error)
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

func (s *MemSessionStore) RevokeSubjectSessions(_ context.Context, subject string) (int, error) {
	return s.deleteSessions(func(sess Session) bool { return sess.Subject == subject }), nil
}

// deleteSessions removes every session matching pred and returns the
// count. Holds the write lock.
func (s *MemSessionStore) deleteSessions(pred func(Session) bool) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for id, sess := range s.sessions {
		if pred(sess) {
			delete(s.sessions, id)
			n++
		}
	}
	return n
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

// NextSessionExpiry computes the expiry a live session should slide to
// when its holder makes a request at now, turning the fixed-lifetime
// session into a rolling one: an active user keeps getting fresh time and
// is never bounced mid-work, while an idle one still lapses idle past the
// current ExpiresAt.
//
// Two knobs bound it. idle is the sliding window — each renewal pushes
// expiry to now+idle. maxAge is the absolute ceiling measured from
// CreatedAt (<= 0 disables it): a continuously-used session can't outlive
// CreatedAt+maxAge, so a leaked token can't be kept alive forever by
// merely being used. The renewed expiry is capped to that ceiling.
//
// The second return reports whether the new expiry is worth persisting. To
// keep steady traffic from writing the store on every request, a renewal
// only fires once the session has entered the second half of its idle
// window (less than idle/2 remaining); before that, and once the absolute
// cap has been reached, it returns the unchanged expiry and false.
func NextSessionExpiry(sess Session, idle, maxAge time.Duration, now time.Time) (time.Time, bool) {
	if idle <= 0 {
		return sess.ExpiresAt, false
	}
	// Only renew in the second half of the idle window — cheap debounce
	// so a busy session isn't rewritten on every call.
	if now.Before(sess.ExpiresAt.Add(-idle / 2)) {
		return sess.ExpiresAt, false
	}
	next := now.Add(idle)
	if maxAge > 0 {
		if cap := sess.CreatedAt.Add(maxAge); next.After(cap) {
			next = cap
		}
	}
	// Never move expiry backwards: clock skew, or the cap already reached
	// (next == cap <= current ExpiresAt) so there's nothing left to extend.
	if !next.After(sess.ExpiresAt) {
		return sess.ExpiresAt, false
	}
	return next, true
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
