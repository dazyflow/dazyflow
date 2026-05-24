package daemon

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"git.sr.ht/~klahr/hazy-flow/core"
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

	mu    sync.Mutex
	cache map[string]quotaEntry // tenant → cached usage
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
	}, nil
}

// SetCacheTTL overrides the default 1s usage cache (tests want zero).
func (q *FSQuota) SetCacheTTL(d time.Duration) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.cacheTTL = d
}

func (q *FSQuota) Limit(tenant string) int64 {
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

// Ensure FSQuota satisfies the interface at compile time.
var _ core.QuotaProvider = (*FSQuota)(nil)
