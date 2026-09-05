// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"sync"
	"sync/atomic"
	"time"
)

// suspensionCache memoizes one moderation answer — "is this account
// locked out" — for a short window. It caches the derived BOOLEAN, never
// the row: the user row carries the password hash, the TOTP secret and
// the recovery codes, and none of those belong in a cache that exists to
// answer a yes/no question.
//
// Bounded the same way CachingSessionStore is: sweep expired entries at
// capacity, and if that doesn't free anything, drop the map. Entries are
// individually re-fetchable, so a periodic cold start is acceptable.
type suspensionCache struct {
	ttl   time.Duration
	max   int
	clock func() time.Time

	mu    sync.Mutex
	items map[string]suspensionEntry

	hits   atomic.Int64
	misses atomic.Int64
}

type suspensionEntry struct {
	suspended bool
	cachedAt  time.Time
}

func newSuspensionCache(ttl time.Duration, max int) *suspensionCache {
	if max <= 0 {
		max = 50_000
	}
	return &suspensionCache{ttl: ttl, max: max, items: make(map[string]suspensionEntry)}
}

func (c *suspensionCache) now() time.Time {
	if c.clock != nil {
		return c.clock()
	}
	return time.Now()
}

// get reports the cached answer for key, and whether there was one.
func (c *suspensionCache) get(key string) (bool, bool) {
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.items[key]
	if !ok {
		c.misses.Add(1)
		return false, false
	}
	if now.Sub(e.cachedAt) >= c.ttl {
		delete(c.items, key)
		c.misses.Add(1)
		return false, false
	}
	c.hits.Add(1)
	return e.suspended, true
}

func (c *suspensionCache) put(key string, suspended bool) {
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.items) >= c.max {
		for k, e := range c.items {
			if now.Sub(e.cachedAt) >= c.ttl {
				delete(c.items, k)
			}
		}
		if len(c.items) >= c.max {
			c.items = make(map[string]suspensionEntry, c.max)
		}
	}
	c.items[key] = suspensionEntry{suspended: suspended, cachedAt: now}
}

func (c *suspensionCache) invalidate(key string) {
	c.mu.Lock()
	delete(c.items, key)
	c.mu.Unlock()
}

func (c *suspensionCache) stats() (hits, misses int64) {
	return c.hits.Load(), c.misses.Load()
}
