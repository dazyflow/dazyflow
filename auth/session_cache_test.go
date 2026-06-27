// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// countingStore is a SessionStore that records how many times each
// method is called, so tests can assert on cache hit/miss behaviour.
type countingStore struct {
	gets, puts, dels atomic.Int64
	sess             Session
	getErr           error
}

func (c *countingStore) GetSession(_ context.Context, _ string) (Session, error) {
	c.gets.Add(1)
	if c.getErr != nil {
		return Session{}, c.getErr
	}
	return c.sess, nil
}

func (c *countingStore) PutSession(_ context.Context, s Session) error {
	c.puts.Add(1)
	c.sess = s
	return nil
}

func (c *countingStore) DeleteSession(_ context.Context, _ string) error {
	c.dels.Add(1)
	return nil
}

func newTestSession(exp time.Time) Session {
	return Session{ID: "abc", Subject: "u@example.com", Tenant: "acme", ExpiresAt: exp}
}

func TestCachingSessionStore_CachesReads(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	inner := &countingStore{sess: newTestSession(now.Add(time.Hour))}
	c := NewCachingSessionStore(inner, 30*time.Second, 0).(*CachingSessionStore)
	c.clock = func() time.Time { return now }

	for i := 0; i < 5; i++ {
		if _, err := c.GetSession(context.Background(), "abc"); err != nil {
			t.Fatalf("get %d: %v", i, err)
		}
	}
	if got := inner.gets.Load(); got != 1 {
		t.Errorf("inner GetSession called %d times, want 1 (rest cached)", got)
	}
}

func TestCachingSessionStore_ExpiresAfterTTL(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	inner := &countingStore{sess: newTestSession(now.Add(time.Hour))}
	c := NewCachingSessionStore(inner, 30*time.Second, 0).(*CachingSessionStore)
	c.clock = func() time.Time { return now }

	_, _ = c.GetSession(context.Background(), "abc")
	now = now.Add(31 * time.Second) // past the cache TTL
	_, _ = c.GetSession(context.Background(), "abc")
	if got := inner.gets.Load(); got != 2 {
		t.Errorf("inner GetSession called %d times, want 2 (cache expired)", got)
	}
}

func TestCachingSessionStore_NeverServesExpiredSession(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	// Session expires in 10s but cache TTL is 30s — the session's own
	// expiry must win so we don't serve a dead session from cache.
	inner := &countingStore{sess: newTestSession(now.Add(10 * time.Second))}
	c := NewCachingSessionStore(inner, 30*time.Second, 0).(*CachingSessionStore)
	c.clock = func() time.Time { return now }

	_, _ = c.GetSession(context.Background(), "abc")
	now = now.Add(15 * time.Second) // past session expiry, within cache TTL
	_, _ = c.GetSession(context.Background(), "abc")
	if got := inner.gets.Load(); got != 2 {
		t.Errorf("inner GetSession called %d times, want 2 (expired session not cached)", got)
	}
}

func TestCachingSessionStore_DeleteEvicts(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	inner := &countingStore{sess: newTestSession(now.Add(time.Hour))}
	c := NewCachingSessionStore(inner, 30*time.Second, 0).(*CachingSessionStore)
	c.clock = func() time.Time { return now }

	_, _ = c.GetSession(context.Background(), "abc") // populate cache
	if err := c.DeleteSession(context.Background(), "abc"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	_, _ = c.GetSession(context.Background(), "abc") // must re-fetch
	if got := inner.gets.Load(); got != 2 {
		t.Errorf("inner GetSession called %d times, want 2 (delete should evict)", got)
	}
	if got := inner.dels.Load(); got != 1 {
		t.Errorf("inner DeleteSession called %d times, want 1", got)
	}
}

func TestCachingSessionStore_DisabledWhenTTLZero(t *testing.T) {
	inner := &countingStore{sess: newTestSession(time.Now().Add(time.Hour))}
	got := NewCachingSessionStore(inner, 0, 0)
	if got != SessionStore(inner) {
		t.Error("ttl<=0 should return the inner store unwrapped")
	}
}
