// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

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

// perIDStore serves a distinct Session per id and implements
// SessionRevoker, so the cache's bounded-eviction and revocation paths
// can be exercised with more than one live entry — countingStore returns
// the same session for every id, which cannot distinguish them.
type perIDStore struct {
	gets atomic.Int64

	mu   sync.Mutex
	sess map[string]Session
}

func newPerIDStore(sessions ...Session) *perIDStore {
	p := &perIDStore{sess: make(map[string]Session, len(sessions))}
	for _, s := range sessions {
		p.sess[s.ID] = s
	}

	return p
}

func (p *perIDStore) GetSession(_ context.Context, id string) (Session, error) {
	p.gets.Add(1)
	p.mu.Lock()
	defer p.mu.Unlock()
	s, ok := p.sess[id]
	if !ok {
		return Session{}, ErrInvalidCredential
	}

	return s, nil
}

func (p *perIDStore) PutSession(_ context.Context, s Session) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sess[s.ID] = s

	return nil
}

func (p *perIDStore) DeleteSession(_ context.Context, id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.sess, id)

	return nil
}

func (p *perIDStore) RevokeSubjectSessions(_ context.Context, subject string) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for id, s := range p.sess {
		if s.Subject == subject {
			delete(p.sess, id)
			n++
		}
	}

	return n, nil
}

func sessionFor(id, subject string, exp time.Time) Session {
	return Session{ID: id, Subject: subject, Tenant: "acme", ExpiresAt: exp}
}

func cachedIDs(c *CachingSessionStore) map[string]bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]bool, len(c.items))
	for id := range c.items {
		out[id] = true
	}

	return out
}

func TestCachingSessionStore_NonPositiveMaxUsesDefaultBound(t *testing.T) {
	// "max <= 0 picks a sane default" — a bound of 0 must not be taken
	// literally, or every put would clear the map and the cache would
	// hold exactly one entry.
	for _, max := range []int{0, -1} {
		now := time.Unix(1_000_000, 0)
		exp := now.Add(time.Hour)
		inner := newPerIDStore(
			sessionFor("a", "u1@example.com", exp),
			sessionFor("b", "u2@example.com", exp),
		)
		c := NewCachingSessionStore(inner, 30*time.Second, max).(*CachingSessionStore)
		c.clock = func() time.Time { return now }

		if c.max != 50_000 {
			t.Errorf("max %d: bound = %d, want 50000 default", max, c.max)
		}
		for _, id := range []string{"a", "b", "a", "b"} {
			if _, err := c.GetSession(context.Background(), id); err != nil {
				t.Fatalf("max %d: get %s: %v", max, id, err)
			}
		}
		if got := inner.gets.Load(); got != 2 {
			t.Errorf("max %d: inner GetSession called %d times, want 2 (both tokens cached)", max, got)
		}
	}
}

func TestCachingSessionStore_ExactTTLAgeIsStale(t *testing.T) {
	// An entry aged exactly the TTL is outside "within the cache TTL":
	// freshness is a strict <, so this must re-fetch.
	now := time.Unix(1_000_000, 0)
	inner := &countingStore{sess: newTestSession(now.Add(time.Hour))}
	c := NewCachingSessionStore(inner, 30*time.Second, 0).(*CachingSessionStore)
	c.clock = func() time.Time { return now }

	_, _ = c.GetSession(context.Background(), "abc")
	now = now.Add(30 * time.Second) // exactly at the TTL, not within it
	_, _ = c.GetSession(context.Background(), "abc")
	if got := inner.gets.Load(); got != 2 {
		t.Errorf("inner GetSession called %d times, want 2 (entry exactly at TTL is stale)", got)
	}
}

func TestCachingSessionStore_RevokeSubjectEvictsOnlyThatSubject(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	exp := now.Add(time.Hour)
	inner := newPerIDStore(
		sessionFor("a", "victim@example.com", exp),
		sessionFor("b", "bystander@example.com", exp),
	)
	c := NewCachingSessionStore(inner, 30*time.Second, 0).(*CachingSessionStore)
	c.clock = func() time.Time { return now }

	_, _ = c.GetSession(context.Background(), "a")
	_, _ = c.GetSession(context.Background(), "b")
	if got := inner.gets.Load(); got != 2 {
		t.Fatalf("setup: inner gets = %d, want 2", got)
	}
	if _, err := c.RevokeSubjectSessions(context.Background(), "victim@example.com"); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	// The bystander's cached entry must survive the revocation.
	if _, err := c.GetSession(context.Background(), "b"); err != nil {
		t.Fatalf("get b: %v", err)
	}
	if got := inner.gets.Load(); got != 2 {
		t.Errorf("inner gets = %d, want 2 (bystander must stay cached)", got)
	}
	if _, err := c.GetSession(context.Background(), "a"); !errors.Is(err, ErrInvalidCredential) {
		t.Errorf("get a after revoke: err = %v, want ErrInvalidCredential", err)
	}
}

func TestCachingSessionStore_BoundedAtMaxEntries(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	exp := now.Add(time.Hour)
	inner := newPerIDStore(
		sessionFor("a", "u@example.com", exp),
		sessionFor("b", "u@example.com", exp),
		sessionFor("c", "u@example.com", exp),
	)
	c := NewCachingSessionStore(inner, 30*time.Second, 2).(*CachingSessionStore)
	c.clock = func() time.Time { return now }

	_, _ = c.GetSession(context.Background(), "a")
	_, _ = c.GetSession(context.Background(), "b") // cache now at max
	_, _ = c.GetSession(context.Background(), "c") // must trigger the bound

	if ids := cachedIDs(c); len(ids) != 1 || !ids["c"] {
		t.Errorf("cached ids = %v, want only {c} once max is reached", ids)
	}
}

func TestCachingSessionStore_SweepDropsEntriesExactlyAtTTL(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	exp := now.Add(time.Hour)
	inner := newPerIDStore(
		sessionFor("a", "u@example.com", exp),
		sessionFor("b", "u@example.com", exp),
		sessionFor("c", "u@example.com", exp),
	)
	c := NewCachingSessionStore(inner, 30*time.Second, 2).(*CachingSessionStore)
	c.clock = func() time.Time { return now }

	_, _ = c.GetSession(context.Background(), "a") // cached at t0
	now = now.Add(30 * time.Second)                // "a" is now exactly at the TTL
	_, _ = c.GetSession(context.Background(), "b") // cached at t0+30, fills to max
	_, _ = c.GetSession(context.Background(), "c") // triggers the sweep

	if ids := cachedIDs(c); len(ids) != 2 || !ids["b"] || !ids["c"] {
		t.Errorf("cached ids = %v, want {b,c} after the sweep", ids)
	}
}
