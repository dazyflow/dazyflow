package auth

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// CachingSessionStore wraps a SessionStore with a small TTL cache for
// GetSession — the hot path hit on every cookie/bearer-authenticated
// request. Without it each API call is a DB round-trip just to validate
// the session token, which dominates the connection pool under load.
//
// Revocation semantics: writes flow through to the inner store AND
// update (or evict) the local cache synchronously, so a sign-out
// (DeleteSession) or rotation (PutSession) on THIS instance takes effect
// immediately. The only staleness is cross-instance — a session revoked
// on instance A can still validate on instance B until B's cached copy
// expires. Keep the TTL short (seconds) so that window is small; it
// trades a brief revocation lag for a large drop in lookup queries. The
// cache never serves a session past its own ExpiresAt regardless of TTL.
type CachingSessionStore struct {
	inner SessionStore
	ttl   time.Duration
	max   int
	clock func() time.Time

	mu    sync.Mutex
	items map[string]cachedSession

	// hits/misses count GetSession outcomes for the /metrics endpoint —
	// the hit ratio shows both that the cache is earning its keep and the
	// shape of authenticated-request load. Atomic so reads don't contend
	// the cache mutex.
	hits   atomic.Int64
	misses atomic.Int64
}

type cachedSession struct {
	sess     Session
	cachedAt time.Time
}

// NewCachingSessionStore wraps inner with a TTL cache. A ttl <= 0
// disables caching entirely (inner is returned unwrapped, so there's no
// overhead when an operator turns it off). max bounds the entry count so
// a flood of distinct tokens can't grow the map without bound; <= 0
// picks a sane default.
func NewCachingSessionStore(inner SessionStore, ttl time.Duration, max int) SessionStore {
	if ttl <= 0 {
		return inner
	}
	if max <= 0 {
		max = 50_000
	}
	return &CachingSessionStore{
		inner: inner,
		ttl:   ttl,
		max:   max,
		items: make(map[string]cachedSession),
	}
}

func (c *CachingSessionStore) now() time.Time {
	if c.clock != nil {
		return c.clock()
	}
	return time.Now()
}

func (c *CachingSessionStore) GetSession(ctx context.Context, id string) (Session, error) {
	now := c.now()
	c.mu.Lock()
	if e, ok := c.items[id]; ok {
		// Fresh while within the cache TTL and before the session's own
		// expiry — an expired session must never be served from cache.
		if now.Sub(e.cachedAt) < c.ttl && now.Before(e.sess.ExpiresAt) {
			c.mu.Unlock()
			c.hits.Add(1)
			return e.sess, nil
		}
		delete(c.items, id)
	}
	c.mu.Unlock()

	c.misses.Add(1)
	sess, err := c.inner.GetSession(ctx, id)
	if err != nil {
		return Session{}, err
	}
	c.put(id, sess, now)
	return sess, nil
}

// Stats returns cumulative cache hit/miss counts for metrics. Safe to
// call concurrently with lookups.
func (c *CachingSessionStore) Stats() (hits, misses int64) {
	return c.hits.Load(), c.misses.Load()
}

func (c *CachingSessionStore) PutSession(ctx context.Context, s Session) error {
	if err := c.inner.PutSession(ctx, s); err != nil {
		return err
	}
	c.put(s.ID, s, c.now())
	return nil
}

func (c *CachingSessionStore) DeleteSession(ctx context.Context, id string) error {
	c.mu.Lock()
	delete(c.items, id)
	c.mu.Unlock()
	return c.inner.DeleteSession(ctx, id)
}

func (c *CachingSessionStore) put(id string, sess Session, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.items) >= c.max {
		// Cheap bound: sweep expired entries first; if still at capacity,
		// drop the whole map. Entries are individually re-fetchable, so a
		// periodic cold start is acceptable and keeps this allocation-free
		// in the common case.
		for k, e := range c.items {
			if now.Sub(e.cachedAt) >= c.ttl || !now.Before(e.sess.ExpiresAt) {
				delete(c.items, k)
			}
		}
		if len(c.items) >= c.max {
			c.items = make(map[string]cachedSession, c.max)
		}
	}
	c.items[id] = cachedSession{sess: sess, cachedAt: now}
}
