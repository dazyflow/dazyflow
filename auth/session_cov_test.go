package auth

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMemSessionStore_DeleteAndRevoke(t *testing.T) {
	ctx := context.Background()
	store := NewMemSessionStore()
	now := time.Now()
	for _, s := range []Session{
		{ID: "s1", Subject: "bob", ExpiresAt: now.Add(time.Hour)},
		{ID: "s2", Subject: "bob", ExpiresAt: now.Add(time.Hour)},
		{ID: "s3", Subject: "alice", ExpiresAt: now.Add(time.Hour)},
	} {
		if err := store.PutSession(ctx, s); err != nil {
			t.Fatalf("PutSession: %v", err)
		}
	}
	// DeleteSession.
	if err := store.DeleteSession(ctx, "s3"); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if _, err := store.GetSession(ctx, "s3"); err != ErrInvalidCredential {
		t.Errorf("deleted session err = %v", err)
	}
	// RevokeSubjectSessions drops both of bob's.
	n, err := store.RevokeSubjectSessions(ctx, "bob")
	if err != nil || n != 2 {
		t.Errorf("RevokeSubjectSessions = %d, %v", n, err)
	}
	if _, err := store.GetSession(ctx, "s1"); err != ErrInvalidCredential {
		t.Errorf("revoked session present: %v", err)
	}
}

func TestSessionAuthenticate_Cov(t *testing.T) {
	ctx := context.Background()
	store := NewMemSessionStore()
	user := User{Subject: "u", Tenant: "t", Workspace: "ws"}
	_, token, err := IssueSession(ctx, store, user, time.Hour)
	if err != nil {
		t.Fatalf("IssueSession: %v", err)
	}
	a := &SessionAuthenticator{Store: store}

	// Wrong prefix → fall through.
	if _, err := a.Authenticate(ctx, "dzk_notasession"); !errors.Is(err, ErrInvalidCredential) {
		t.Errorf("non-session token err = %v", err)
	}
	// Unknown token.
	if _, err := a.Authenticate(ctx, SessionTokenPrefix+"deadbeef"); !errors.Is(err, ErrInvalidCredential) {
		t.Errorf("unknown token err = %v", err)
	}
	// Valid token authenticates.
	if p, err := a.Authenticate(ctx, token); err != nil || p.Subject != "u" {
		t.Errorf("valid token = %+v, %v", p, err)
	}
}

func TestSessionAuthenticate_ExpiredDeletes(t *testing.T) {
	ctx := context.Background()
	store := NewMemSessionStore()
	user := User{Subject: "u", Tenant: "t"}
	sess, token, err := IssueSession(ctx, store, user, time.Hour)
	if err != nil {
		t.Fatalf("IssueSession: %v", err)
	}
	// Clock past expiry → expired error, and the session is deleted.
	a := &SessionAuthenticator{Store: store, Clock: func() time.Time { return sess.ExpiresAt.Add(time.Hour) }}
	if _, err := a.Authenticate(ctx, token); !errors.Is(err, ErrInvalidCredential) {
		t.Errorf("expired token err = %v", err)
	}
	if _, err := store.GetSession(ctx, sess.ID); err != ErrInvalidCredential {
		t.Errorf("expired session not deleted: %v", err)
	}
}

func TestCachingSessionStore_FullSurface(t *testing.T) {
	ctx := context.Background()
	inner := NewMemSessionStore()
	wrapped := NewCachingSessionStore(inner, time.Minute, 0)
	c, ok := wrapped.(*CachingSessionStore)
	if !ok {
		t.Fatalf("expected *CachingSessionStore, got %T", wrapped)
	}
	now := time.Now()
	sess := Session{ID: "s1", Subject: "bob", ExpiresAt: now.Add(time.Hour)}

	// PutSession flows through and caches.
	if err := c.PutSession(ctx, sess); err != nil {
		t.Fatalf("PutSession: %v", err)
	}
	// GetSession served from cache (hit).
	if _, err := c.GetSession(ctx, "s1"); err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	hits, _ := c.Stats()
	if hits == 0 {
		t.Error("expected a cache hit")
	}

	// RevokeSubjectSessions forwards + evicts.
	c.PutSession(ctx, Session{ID: "s2", Subject: "bob", ExpiresAt: now.Add(time.Hour)})
	n, err := c.RevokeSubjectSessions(ctx, "bob")
	if err != nil || n != 2 {
		t.Errorf("RevokeSubjectSessions = %d, %v", n, err)
	}
	if _, err := inner.GetSession(ctx, "s1"); err != ErrInvalidCredential {
		t.Errorf("session not revoked in inner store: %v", err)
	}

	// DeleteSession evicts + forwards.
	c.PutSession(ctx, sess)
	if err := c.DeleteSession(ctx, "s1"); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if _, err := inner.GetSession(ctx, "s1"); err != ErrInvalidCredential {
		t.Errorf("delete not forwarded: %v", err)
	}
}

func TestCachingSessionStore_RevokeWithoutRevoker(t *testing.T) {
	// An inner store that is NOT a SessionRevoker → RevokeSubjectSessions
	// returns 0, nil.
	wrapped := NewCachingSessionStore(plainSessionStore{NewMemSessionStore()}, time.Minute, 0)
	c := wrapped.(*CachingSessionStore)
	n, err := c.RevokeSubjectSessions(context.Background(), "bob")
	if err != nil || n != 0 {
		t.Errorf("RevokeSubjectSessions without revoker = %d, %v", n, err)
	}
}

// plainSessionStore wraps a store but does NOT expose RevokeSubjectSessions,
// so the type assertion in CachingSessionStore fails.
type plainSessionStore struct{ inner *MemSessionStore }

func (p plainSessionStore) GetSession(ctx context.Context, id string) (Session, error) {
	return p.inner.GetSession(ctx, id)
}
func (p plainSessionStore) PutSession(ctx context.Context, s Session) error {
	return p.inner.PutSession(ctx, s)
}
func (p plainSessionStore) DeleteSession(ctx context.Context, id string) error {
	return p.inner.DeleteSession(ctx, id)
}

func TestCachingSessionStore_PutEvictsAtCapacity(t *testing.T) {
	ctx := context.Background()
	inner := NewMemSessionStore()
	wrapped := NewCachingSessionStore(inner, time.Minute, 1)
	c := wrapped.(*CachingSessionStore)
	now := time.Now()
	// First entry fills the single-slot cache.
	c.PutSession(ctx, Session{ID: "s1", Subject: "a", ExpiresAt: now.Add(time.Hour)})
	// Second insert hits the capacity sweep path.
	c.PutSession(ctx, Session{ID: "s2", Subject: "b", ExpiresAt: now.Add(time.Hour)})
	// Both are still retrievable from the inner store regardless.
	if _, err := c.GetSession(ctx, "s2"); err != nil {
		t.Errorf("GetSession s2: %v", err)
	}
}
