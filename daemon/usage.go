// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"
)

// Usage metering (T3 / Phase 3): per-tenant counts of graph runs and
// node executions, bucketed by calendar month (UTC). The counters are
// the raw material for the usage page today and for Stripe plan gates
// later ("free tier: 100 runs/month").
//
// Buckets are keyed "YYYY-MM" rather than reset in place: a billing-day
// boundary (tenants whose cycle starts mid-month) can be layered on at
// query time once plans carry a billing anchor — the writes don't change.
//
// Recording is best-effort by contract: callers MUST NOT fail a run or a
// node completion because metering failed. Implementations return the
// error for logging only.

type UsageCounters struct {
	Period         string `json:"period"` // "2026-06" (UTC month)
	GraphRuns      int64  `json:"graph_runs"`
	NodeExecutions int64  `json:"node_executions"`
	// SkippedRuns counts scheduled fires the run-cap gate refused this
	// month — otherwise an invisible, log-only event. Surfaced so a capped
	// tenant learns why their schedules stopped.
	SkippedRuns int64 `json:"skipped_runs"`
}

// UsageStore records and reads per-tenant usage. Implementations must be
// safe for concurrent use (every worker goroutine records through one
// store) and increments must be atomic across replicas for the Postgres
// backend.
type UsageStore interface {
	AddRun(ctx context.Context, tenant string, now time.Time) error
	AddNodeExecutions(ctx context.Context, tenant string, n int, now time.Time) error
	AddSkippedRun(ctx context.Context, tenant string, now time.Time) error
	Usage(ctx context.Context, tenant string, months int) ([]UsageCounters, error)
}

// runReleaser is an optional UsageStore extension that gives back a reserved
// run. The reservation happens BEFORE the run is written, because the cap
// check and the increment have to be one atomic step or concurrent
// submissions at the limit all pass — but that leaves a window where the
// write then fails and the tenant has been charged for a run that does not
// exist anywhere. Releasing closes it.
//
// Best-effort by nature: whatever broke the run's write may well have broken
// this too. Worth having anyway — a jobs-table conflict with a healthy usage
// table is exactly the case it recovers, and over-counting somebody's monthly
// allowance is not a rounding error to them.
type runReleaser interface {
	ReleaseRun(ctx context.Context, tenant string, now time.Time) error
}

// runReserver is an optional UsageStore extension: atomically count a run only
// if the tenant is still under its monthly cap. The real stores (Mem, Pg)
// implement it so the run-cap gate is a single atomic check-and-increment
// rather than a racy read-then-add that lets concurrent submissions at the
// limit all pass. A store without it falls back to the racy path in reserveRun.
type runReserver interface {
	AddRunIfUnder(ctx context.Context, tenant string, now time.Time, limit int) (admitted bool, err error)
}

func usagePeriod(t time.Time) string {
	return t.UTC().Format("2006-01")
}

type MemUsageStore struct {
	mu      sync.Mutex
	buckets map[string]map[string]*UsageCounters // tenant → period → counters
}

func NewMemUsageStore() *MemUsageStore {
	return &MemUsageStore{buckets: map[string]map[string]*UsageCounters{}}
}

func (m *MemUsageStore) bucket(tenant string, now time.Time) *UsageCounters {
	period := usagePeriod(now)
	byPeriod, ok := m.buckets[tenant]
	if !ok {
		byPeriod = map[string]*UsageCounters{}
		m.buckets[tenant] = byPeriod
	}
	c, ok := byPeriod[period]
	if !ok {
		c = &UsageCounters{Period: period}
		byPeriod[period] = c
	}
	return c
}

func (m *MemUsageStore) AddRun(_ context.Context, tenant string, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.bucket(tenant, now).GraphRuns++
	return nil
}

func (m *MemUsageStore) AddRunIfUnder(_ context.Context, tenant string, now time.Time, limit int) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b := m.bucket(tenant, now)
	if limit > 0 && b.GraphRuns >= int64(limit) {
		return false, nil
	}
	b.GraphRuns++
	return true, nil
}

// ReleaseRun implements runReleaser. Floored at zero: a release without a
// matching reserve must not push a bucket negative and make the Usage page
// nonsense.
func (m *MemUsageStore) ReleaseRun(_ context.Context, tenant string, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	b := m.bucket(tenant, now)
	if b.GraphRuns > 0 {
		b.GraphRuns--
	}
	return nil
}

func (m *MemUsageStore) AddNodeExecutions(_ context.Context, tenant string, n int, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.bucket(tenant, now).NodeExecutions += int64(n)
	return nil
}

func (m *MemUsageStore) AddSkippedRun(_ context.Context, tenant string, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.bucket(tenant, now).SkippedRuns++
	return nil
}

func (m *MemUsageStore) Usage(_ context.Context, tenant string, months int) ([]UsageCounters, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]UsageCounters, 0, len(m.buckets[tenant]))
	for _, c := range m.buckets[tenant] {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Period > out[j].Period })
	if months > 0 && len(out) > months {
		out = out[:months]
	}
	return out, nil
}

// BufferedUsage batches node-execution counts in memory and flushes
// them to the inner store on an interval. Without it every executed
// node attempt is a synchronous upsert against the SAME (tenant, month)
// row — a lock-contention point once many workers serve one busy
// tenant. Runs pass through unbatched (they're far rarer, and the run
// gate reads them); reads flush first so the gate and the Usage page
// never lag behind by more than the in-flight call. Losing one unflushed
// window on crash is within the metering's documented best-effort
// contract.
type BufferedUsage struct {
	inner UsageStore

	mu      sync.Mutex
	pending map[string]int // tenant + "\x00" + period → executions
}

func NewBufferedUsage(inner UsageStore) *BufferedUsage {
	return &BufferedUsage{inner: inner, pending: map[string]int{}}
}

func (b *BufferedUsage) AddRun(ctx context.Context, tenant string, now time.Time) error {
	return b.inner.AddRun(ctx, tenant, now)
}

// AddRunIfUnder forwards to the inner store's atomic reserve. Runs are never
// buffered (the comment above: they're rare and the gate reads them), so this
// is a straight passthrough; a non-reserver inner falls back to read-then-add.
// ReleaseRun implements runReleaser by delegating; a buffered store that
// cannot release simply does not, and the reservation stands.
func (b *BufferedUsage) ReleaseRun(ctx context.Context, tenant string, now time.Time) error {
	if rl, ok := b.inner.(runReleaser); ok {
		return rl.ReleaseRun(ctx, tenant, now)
	}
	return nil
}

func (b *BufferedUsage) AddRunIfUnder(ctx context.Context, tenant string, now time.Time, limit int) (bool, error) {
	if rr, ok := b.inner.(runReserver); ok {
		return rr.AddRunIfUnder(ctx, tenant, now, limit)
	}
	buckets, err := b.inner.Usage(ctx, tenant, 1)
	if err != nil {
		return false, err
	}
	if len(buckets) > 0 && buckets[0].Period == usagePeriod(now) && buckets[0].GraphRuns >= int64(limit) {
		return false, nil
	}
	return true, b.inner.AddRun(ctx, tenant, now)
}

func (b *BufferedUsage) AddSkippedRun(ctx context.Context, tenant string, now time.Time) error {
	return b.inner.AddSkippedRun(ctx, tenant, now)
}

func (b *BufferedUsage) AddNodeExecutions(_ context.Context, tenant string, n int, now time.Time) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pending[tenant+"\x00"+usagePeriod(now)] += n
	return nil
}

func (b *BufferedUsage) Usage(ctx context.Context, tenant string, months int) ([]UsageCounters, error) {
	// Flush first so reads never lag by more than the in-flight call. A
	// failed flush already re-queued its counts; the read proceeds on
	// whatever the inner store has.
	_ = b.Flush(ctx)
	return b.inner.Usage(ctx, tenant, months)
}

func (b *BufferedUsage) Flush(ctx context.Context) error {
	var firstErr error
	for key, n := range b.snapshot() {
		tenant, periodKey, _ := strings.Cut(key, "\x00")
		ts, err := time.Parse("2006-01", periodKey)
		if err != nil {
			continue // unreachable: keys are built from usagePeriod
		}
		if err := b.inner.AddNodeExecutions(ctx, tenant, n, ts); err != nil {
			b.mu.Lock()
			b.pending[key] += n
			b.mu.Unlock()
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// snapshot drains the pending map under the lock.
func (b *BufferedUsage) snapshot() map[string]int {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := b.pending
	b.pending = map[string]int{}
	return out
}

func (b *BufferedUsage) Run(ctx context.Context, every time.Duration) {
	if every <= 0 {
		every = 5 * time.Second
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = b.Flush(context.WithoutCancel(ctx))
			return
		case <-t.C:
			if err := b.Flush(ctx); err != nil {
				continue
			}
		}
	}
}
