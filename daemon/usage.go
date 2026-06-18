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
// in-memory stores, and dzd already logs the loud "lost on restart"
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

// Flush writes all pending counts through. Counts that fail to write
// are re-queued so a transient store error loses nothing.
func (b *BufferedUsage) Flush(ctx context.Context) error {
	var firstErr error
	for key, n := range b.snapshot() {
		tenant, periodKey, _ := strings.Cut(key, "\x00")
		// Reconstruct a timestamp inside the bucket's month so the
		// inner store lands the count in the right period.
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

// Run flushes every interval until ctx is cancelled, then flushes one
// last time so a graceful shutdown loses nothing.
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
				// Logged by callers' stores already; nothing more to do —
				// the counts are re-queued.
				continue
			}
		}
	}
}
