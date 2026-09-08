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

// Per-tenant counts of graph runs and node executions.

type UsageCounters struct {
	Period         string `json:"period"` // "2026-06" (UTC month)
	GraphRuns      int64  `json:"graph_runs"`
	NodeExecutions int64  `json:"node_executions"`
	// Refused fires are counted, or an org over its cap looks simply idle.
	SkippedRuns int64 `json:"skipped_runs"`
}

// Implementations must be safe for concurrent use.
type UsageStore interface {
	AddRun(ctx context.Context, tenant string, now time.Time) error
	AddNodeExecutions(ctx context.Context, tenant string, n int, now time.Time) error
	AddSkippedRun(ctx context.Context, tenant string, now time.Time) error
	Usage(ctx context.Context, tenant string, months int) ([]UsageCounters, error)
}

// Gives back a reservation when the submission that took it then failed.
type runReleaser interface {
	ReleaseRun(ctx context.Context, tenant string, now time.Time) error
}

// Atomic: a gap between the cap check and the increment lets two concurrent
// submissions both pass a cap with one slot left.
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

// Floored at zero: a release without a matching reserve must not go negative.
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

// Node counts are high-frequency, so they are batched rather than written per step.
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

// Runs are never buffered: the cap has to be authoritative at submit time.
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
	// Flush first, or a read lags behind what has already been counted.
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
