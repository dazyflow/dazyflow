package io

import "sync"

// quotaReserver is the cross-cutting hook the daemon installs at startup
// to give the in-process filesystem drops an atomic reserve-and-hold
// against per-tenant disk quotas. It mirrors the SetTokenLookup /
// SetSecretWriter / SetEgressAllowlist wiring used elsewhere: the
// integrations layer stays free of a daemon import, and dzd injects a
// closure over FSQuota.Reserve. When no reserver is installed (no quota
// provider, or in unit tests), reserveQuota is a no-op that always
// succeeds — the drops fall back to the per-job snapshot check.
var (
	quotaMu       sync.RWMutex
	quotaReserver func(tenant string, n int64) (release func(), err error)
)

// SetQuotaReserver installs (or clears, with nil) the quota reservation
// hook. Called once at daemon startup.
func SetQuotaReserver(fn func(tenant string, n int64) (release func(), err error)) {
	quotaMu.Lock()
	defer quotaMu.Unlock()
	quotaReserver = fn
}

// reserveQuota reserves n bytes against tenant's budget, returning a
// release to call once the write completes or fails. When no reserver is
// installed it returns a no-op release and a nil error so callers can
// always defer release() unconditionally.
func reserveQuota(tenant string, n int64) (func(), error) {
	quotaMu.RLock()
	fn := quotaReserver
	quotaMu.RUnlock()
	if fn == nil {
		return func() {}, nil
	}
	return fn(tenant, n)
}
