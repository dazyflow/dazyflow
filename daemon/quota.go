// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
)

// FSQuota tracks per-tenant disk usage by walking the tenant directory
// under the sandbox base. It pairs naturally with FSSandbox.
//
// Limits are configured once at construction. Used() walks the tenant's
// subtree and caches the result for CacheTTL (1s by default) to keep the
// cost predictable when a graph fires many jobs back-to-back.
type FSQuota struct {
	base     string
	limits   map[string]int64
	cacheTTL time.Duration

	// LimitOverride, when set, supplies a per-tenant byte limit that takes
	// precedence over the configured map (a non-positive return falls back
	// to the map). The daemon wires it to the entitlement resolver so a
	// platform admin's per-org/tier disk quota is honoured. Called outside
	// q.mu, so it must not call back into FSQuota.
	LimitOverride func(tenant string) int64

	mu       sync.Mutex
	cache    map[string]quotaEntry // tenant → cached usage
	inflight map[string]int64      // tenant → bytes reserved but not yet committed
}

type quotaEntry struct {
	used    int64
	expires time.Time
}

// NewFSQuota wires a quota provider against the same base directory the
// FSSandbox uses. limits maps tenant → bytes; 0 (or missing) means
// unlimited.
func NewFSQuota(base string, limits map[string]int64) (*FSQuota, error) {
	if base == "" {
		return nil, fmt.Errorf("FSQuota: base directory required")
	}
	if _, err := os.Stat(base); err != nil {
		return nil, fmt.Errorf("stat sandbox base %q: %w", base, err)
	}
	abs, err := filepath.Abs(base)
	if err != nil {
		return nil, err
	}
	cp := make(map[string]int64, len(limits))
	for k, v := range limits {
		cp[k] = v
	}
	return &FSQuota{
		base:     abs,
		limits:   cp,
		cacheTTL: time.Second,
		cache:    make(map[string]quotaEntry),
		inflight: make(map[string]int64),
	}, nil
}

// SetCacheTTL overrides the default 1s usage cache (tests want zero).
func (q *FSQuota) SetCacheTTL(d time.Duration) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.cacheTTL = d
}

func (q *FSQuota) Limit(tenant string) int64 {
	if q.LimitOverride != nil {
		if v := q.LimitOverride(tenant); v > 0 {
			return v
		}
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.limits[tenant]
}

func (q *FSQuota) Used(tenant string) (int64, error) {
	q.mu.Lock()
	if entry, ok := q.cache[tenant]; ok && time.Now().Before(entry.expires) {
		q.mu.Unlock()
		return entry.used, nil
	}
	ttl := q.cacheTTL
	q.mu.Unlock()

	used, err := walkUsage(filepath.Join(q.base, tenant))
	if err != nil {
		return 0, err
	}
	if ttl > 0 {
		q.mu.Lock()
		q.cache[tenant] = quotaEntry{used: used, expires: time.Now().Add(ttl)}
		q.mu.Unlock()
	}
	return used, nil
}

// usedLocked returns tenant usage using the cache when fresh, otherwise
// walking the tenant subtree and repopulating the cache. The caller MUST
// hold q.mu — the walk runs under the lock so Reserve observes a
// consistent (used, inflight) pair. Walks are cacheTTL-gated, so the
// lock is rarely held for the duration of an actual walk.
func (q *FSQuota) usedLocked(tenant string) (int64, error) {
	if entry, ok := q.cache[tenant]; ok && time.Now().Before(entry.expires) {
		return entry.used, nil
	}
	used, err := walkUsage(filepath.Join(q.base, tenant))
	if err != nil {
		return 0, err
	}
	if q.cacheTTL > 0 {
		q.cache[tenant] = quotaEntry{used: used, expires: time.Now().Add(q.cacheTTL)}
	}
	return used, nil
}

// Reserve implements core.QuotaReserver. It atomically checks that the
// tenant's committed usage plus its outstanding reservations plus n fits
// within the limit, and if so records n as in-flight, returning a release
// to free it. The reservation is what closes the concurrent-write race
// the bare Used() snapshot can't: a second writer sees the first's
// not-yet-committed bytes in q.inflight.
//
// The lock is held only across the check+increment, not across the
// caller's disk write — holding the in-flight count is what "reserves"
// the budget, so the write itself runs unserialized. On release we drop
// the in-flight bytes and invalidate the usage cache so the next Reserve
// re-walks and sees the bytes this write committed (avoiding a window
// where stale-cached used + reduced inflight would under-count).
func (q *FSQuota) Reserve(tenant string, n int64) (func(), error) {
	// Resolve the override before taking the lock (it may call into the
	// entitlement store, which has its own lock).
	override := int64(0)
	if q.LimitOverride != nil {
		override = q.LimitOverride(tenant)
	}
	q.mu.Lock()
	limit := q.limits[tenant]
	if override > 0 {
		limit = override
	}
	if limit <= 0 { // unlimited tenant — nothing to enforce
		q.mu.Unlock()
		return func() {}, nil
	}
	used, err := q.usedLocked(tenant)
	if err != nil {
		q.mu.Unlock()
		return nil, err
	}
	if used+q.inflight[tenant]+n > limit {
		exceeded := fmt.Errorf("%w: reserve of %d bytes would push tenant %q to %d past limit %d (committed %d, in-flight %d)",
			core.ErrQuotaExceeded, n, tenant, used+q.inflight[tenant]+n, limit, used, q.inflight[tenant])
		q.mu.Unlock()
		return nil, exceeded
	}
	q.inflight[tenant] += n
	q.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			q.mu.Lock()
			if q.inflight[tenant] -= n; q.inflight[tenant] <= 0 {
				delete(q.inflight, tenant)
			}
			delete(q.cache, tenant) // force a fresh walk that includes the committed write
			q.mu.Unlock()
		})
	}, nil
}

// Usage implements core.QuotaReporter: a usage reading for every tenant
// with a configured limit. Used (cache-aware) is read per tenant;
// best-effort, so a tenant whose subtree can't be walked is skipped
// rather than failing the whole snapshot. Tenants without a configured
// limit (unlimited) aren't reported — the "approaching limit" signal
// only applies where a limit exists.
func (q *FSQuota) Usage() []core.QuotaUsage {
	q.mu.Lock()
	limits := make(map[string]int64, len(q.limits))
	for t, l := range q.limits {
		limits[t] = l
	}
	q.mu.Unlock()

	tenants := make([]string, 0, len(limits))
	for t := range limits {
		tenants = append(tenants, t)
	}
	sort.Strings(tenants)

	out := make([]core.QuotaUsage, 0, len(tenants))
	for _, t := range tenants {
		used, err := q.Used(t) // own locking; cache-aware
		if err != nil {
			continue
		}
		out = append(out, core.QuotaUsage{Tenant: t, Used: used, Limit: limits[t]})
	}
	return out
}

// Invalidate clears the cached usage for tenant. Tests that mutate files
// outside the standard write path call this so the next Used() re-walks.
func (q *FSQuota) Invalidate(tenant string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.cache, tenant)
}

func walkUsage(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		total += info.Size()
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		// Tenant directory doesn't exist yet — no usage.
		return 0, nil
	}
	return total, err
}

// Ensure FSQuota satisfies the interfaces at compile time.
var (
	_ core.QuotaProvider = (*FSQuota)(nil)
	_ core.QuotaReserver = (*FSQuota)(nil)
	_ core.QuotaReporter = (*FSQuota)(nil)
)
