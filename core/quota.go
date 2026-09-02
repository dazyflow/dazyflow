// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import "errors"

// QuotaProvider tells the engine the byte budget for each tenant. The
// engine snapshots Limit and Used at job-start time and stows them on the
// Job so modules can refuse writes that would push the tenant over.
//
// Production-grade enforcement should be paired with OS-level quotas
// (XFS project quotas, ZFS quotas, cgroups blkio) because the in-process
// snapshot check can't see writes still in flight. A provider that also
// implements QuotaReserver closes the in-process concurrent-write race;
// the OS-level pairing remains the backstop for out-of-process writers.
type QuotaProvider interface {
	// Limit returns the byte budget for tenant. Zero means unlimited.
	Limit(tenant string) int64

	// Used returns the bytes currently held by tenant. Caller treats it
	// as a snapshot; staleness is acceptable given the documented race.
	Used(tenant string) (int64, error)
}

// QuotaUsage is a point-in-time disk-usage reading for one tenant,
// used by the metrics endpoint to surface approaching-limit signals.
type QuotaUsage struct {
	Tenant string
	Used   int64
	Limit  int64 // 0 = unlimited
}

// QuotaReporter is an optional extension of QuotaProvider that can
// enumerate per-tenant usage for observability. Providers that don't
// implement it simply expose no per-tenant gauges.
type QuotaReporter interface {
	QuotaProvider

	// Usage returns a usage reading per known tenant (those with a
	// configured limit). Best-effort: a tenant whose usage can't be read
	// is omitted rather than failing the whole snapshot.
	Usage() []QuotaUsage
}

// ErrQuotaExceeded is returned by QuotaReserver.Reserve when a write
// would push the tenant past its budget. Callers match it with
// errors.Is to surface a friendly "quota_exceeded" result.
var ErrQuotaExceeded = errors.New("quota exceeded")

// ErrGraphTooLarge is returned when a submitted graph exceeds one of the
// operator's size ceilings — node count or connection count — a
// resource-exhaustion guard checked before any run state is allocated.
var ErrGraphTooLarge = errors.New("graph exceeds size limit")

// ErrTriggerLoop is returned when a submission is refused because the
// trigger chain that reached it is too deep — a flow triggering itself,
// directly or through another flow. Callers match it with errors.Is to
// answer 429 rather than a generic error.
var ErrTriggerLoop = errors.New("trigger loop")

// ErrPlanLimit is returned when a run submission is rejected by the
// tenant's billing plan (free-tier monthly run cap). Callers match it
// with errors.Is to surface an upgrade prompt (HTTP 402) instead of a
// generic bad-request error.
var ErrPlanLimit = errors.New("plan limit reached")

// ErrOrgSuspended is returned when a run is refused because a platform
// admin has suspended the org. Callers match it with errors.Is to
// surface a 403 lockout rather than a generic error; the scheduler logs
// and skips it.
var ErrOrgSuspended = errors.New("organization suspended")

// QuotaReserver is an optional extension of QuotaProvider for providers
// that can atomically reserve bytes against a tenant's budget. It closes
// the TOCTOU race the bare Used() snapshot can't: two concurrent writes
// from the same tenant each pass the stale snapshot check and together
// bust the limit. Reserve counts the bytes as in-flight under a lock so
// concurrent reservers see each other's pending writes.
//
// The write path calls Reserve before mutating disk and the returned
// release once the write completes (or fails), freeing the reservation.
type QuotaReserver interface {
	QuotaProvider

	// Reserve atomically checks that the tenant's current usage plus all
	// outstanding reservations plus n stays within Limit and, if so,
	// records n as in-flight, returning a release func to free it.
	// Returns ErrQuotaExceeded when the write wouldn't fit. An unlimited
	// tenant (Limit == 0) always succeeds with a no-op release.
	Reserve(tenant string, n int64) (release func(), err error)
}
