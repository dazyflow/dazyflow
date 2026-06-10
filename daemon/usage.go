package daemon

import (
	"context"
	"sort"
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

// UsageCounters is one tenant-month bucket.
type UsageCounters struct {
	Period         string `json:"period"` // "2026-06" (UTC month)
	GraphRuns      int64  `json:"graph_runs"`
	NodeExecutions int64  `json:"node_executions"`
}

// UsageStore records and reads per-tenant usage. Implementations must be
// safe for concurrent use (every worker goroutine records through one
// store) and increments must be atomic across replicas for the Postgres
// backend.
type UsageStore interface {
	// AddRun counts one submitted graph run for the tenant, in the
	// month bucket containing now.
	AddRun(ctx context.Context, tenant string, now time.Time) error
	// AddNodeExecutions counts n executed node attempts for the tenant,
	// in the month bucket containing now.
	AddNodeExecutions(ctx context.Context, tenant string, n int, now time.Time) error
	// Usage returns the tenant's most recent buckets, newest first, at
	// most months entries. Months with no activity have no bucket; the
	// caller synthesizes zeros where it wants them.
	Usage(ctx context.Context, tenant string, months int) ([]UsageCounters, error)
}

// usagePeriod buckets a timestamp into its UTC calendar month.
func usagePeriod(t time.Time) string {
	return t.UTC().Format("2006-01")
}

// MemUsageStore is the in-process UsageStore for dev/no-DSN deployments
// and tests. Counts vanish on restart — same caveat as the rest of the
// in-memory stores, and hzd already logs the loud "lost on restart"
// warning when running without Postgres.
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

func (m *MemUsageStore) AddNodeExecutions(_ context.Context, tenant string, n int, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.bucket(tenant, now).NodeExecutions += int64(n)
	return nil
}

func (m *MemUsageStore) Usage(_ context.Context, tenant string, months int) ([]UsageCounters, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]UsageCounters, 0, len(m.buckets[tenant]))
	for _, c := range m.buckets[tenant] {
		out = append(out, *c)
	}
	// "YYYY-MM" sorts chronologically as a string; newest first.
	sort.Slice(out, func(i, j int) bool { return out[i].Period > out[j].Period })
	if months > 0 && len(out) > months {
		out = out[:months]
	}
	return out, nil
}
