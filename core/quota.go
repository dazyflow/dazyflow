// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import "errors"

// Pair with OS-level quotas in production: the in-process snapshot cannot see
// writes still in flight. QuotaReserver closes the in-process race, but the
// OS-level pairing remains the backstop for out-of-process writers.
type QuotaProvider interface {
	Limit(tenant string) int64

	// A snapshot; staleness is acceptable given the race above.
	Used(tenant string) (int64, error)
}

type QuotaUsage struct {
	Tenant string
	Used   int64
	Limit  int64 // 0 = unlimited
}

type QuotaReporter interface {
	QuotaProvider

	Usage() []QuotaUsage
}

var ErrQuotaExceeded = errors.New("quota exceeded")

var ErrGraphTooLarge = errors.New("graph exceeds size limit")

var ErrTriggerLoop = errors.New("trigger loop")

var ErrPlanLimit = errors.New("plan limit reached")

var ErrOrgSuspended = errors.New("organization suspended")

// Closes the TOCTOU race the bare Used snapshot cannot: two concurrent writes
// from one tenant each pass the stale check and together bust the limit. Reserve
// counts bytes in-flight under a lock, so concurrent reservers see each other.
type QuotaReserver interface {
	QuotaProvider

	Reserve(tenant string, n int64) (release func(), err error)
}
