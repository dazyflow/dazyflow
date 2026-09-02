// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
	"github.com/jackc/pgx/v5/pgxpool"
)

// metricsAPI serves the Prometheus metrics endpoints. Its fields are the whole of what
// those handlers touch.
type metricsAPI struct {
	svc      *Service
	Sessions auth.SessionStore
	Metrics  *Metrics
	DBPool   *pgxpool.Pool
}

// metricsAPI builds them from the gateway's configuration.
func (h *HTTPGateway) metricsAPI() *metricsAPI {
	return &metricsAPI{svc: h.svc, Sessions: h.Sessions, Metrics: h.Metrics, DBPool: h.DBPool}
}

// queueAger is the optional JobStore capability for reporting queue
// latency. The Postgres store implements it; the in-memory dev store
// doesn't, so the gauge is simply omitted there.
type queueAger interface {
	OldestQueuedEnqueuedAt(ctx context.Context) (time.Time, bool, error)
}

// sessionCacheStatter is implemented by auth.CachingSessionStore. When
// the session store is wrapped with the cache, the metrics endpoint
// surfaces its hit/miss counters; otherwise the gauges are omitted.
type sessionCacheStatter interface {
	Stats() (hits, misses int64)
}

// jobStatusOrder fixes the gauge emission order so every status (incl.
// zero counts) appears every scrape — stable, gap-free time series.
var jobStatusOrder = []core.JobStatus{
	core.JobStatusQueued,
	core.JobStatusRunning,
	core.JobStatusAwaiting,
	core.JobStatusSucceeded,
	core.JobStatusFailed,
	core.JobStatusCancelled,
	core.JobStatusSkipped,
}

// metrics serves a minimal Prometheus text exposition. It's hand-rolled
// (no client_golang dependency) because the surface is small: a liveness
// gauge plus per-tenant disk usage. Unauthenticated by design — it's a
// scrape endpoint, gated behind EnableMetrics and meant to be reachable
// only from the operator's monitoring network.
func (h *metricsAPI) metrics(rw http.ResponseWriter, r *http.Request) {
	rw.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

	fmt.Fprint(rw, "# HELP dazyflow_up 1 when the daemon is serving.\n")
	fmt.Fprint(rw, "# TYPE dazyflow_up gauge\n")
	fmt.Fprint(rw, "dazyflow_up 1\n")

	// Node-job counts by status — queue depth (queued) + in-flight
	// (running) are the load-bearing signals.
	if counter, ok := h.svc.Jobs.(core.JobCounter); ok {
		if counts, err := counter.CountsByStatus(r.Context()); err == nil {
			fmt.Fprint(rw, "# HELP dazyflow_jobs Node-job records currently in the store, by status.\n")
			fmt.Fprint(rw, "# TYPE dazyflow_jobs gauge\n")
			for _, status := range jobStatusOrder {
				fmt.Fprintf(rw, "dazyflow_jobs{status=%s} %d\n", promLabel(string(status)), counts[status])
			}
		}
	}

	// Queue latency: how long the oldest claimable node job has waited.
	// A rising value is the early signal that workers can't keep up —
	// raise DAZYFLOW_WORKER_COUNT or add replicas before users feel it.
	if ager, ok := h.svc.Jobs.(queueAger); ok {
		if t, present, err := ager.OldestQueuedEnqueuedAt(r.Context()); err == nil {
			age := 0.0
			if present {
				age = time.Since(t).Seconds()
			}
			fmt.Fprint(rw, "# HELP dazyflow_jobs_oldest_queued_seconds Age of the oldest claimable node job (0 when the queue is empty).\n")
			fmt.Fprint(rw, "# TYPE dazyflow_jobs_oldest_queued_seconds gauge\n")
			fmt.Fprintf(rw, "dazyflow_jobs_oldest_queued_seconds %.3f\n", age)
		}
	}

	// Postgres pool saturation — the earliest warning that the pool is
	// undersized. empty_acquires climbing means callers are waiting for a
	// free connection (raise DAZYFLOW_PG_MAX_CONNS or scale out).
	if h.DBPool != nil {
		st := h.DBPool.Stat()
		fmt.Fprint(rw, "# HELP dazyflow_pg_pool_connections Postgres pool connections by state.\n")
		fmt.Fprint(rw, "# TYPE dazyflow_pg_pool_connections gauge\n")
		fmt.Fprintf(rw, "dazyflow_pg_pool_connections{state=%s} %d\n", promLabel("acquired"), st.AcquiredConns())
		fmt.Fprintf(rw, "dazyflow_pg_pool_connections{state=%s} %d\n", promLabel("idle"), st.IdleConns())
		fmt.Fprintf(rw, "dazyflow_pg_pool_connections{state=%s} %d\n", promLabel("total"), st.TotalConns())
		fmt.Fprint(rw, "# HELP dazyflow_pg_pool_max_connections Configured pool ceiling.\n")
		fmt.Fprint(rw, "# TYPE dazyflow_pg_pool_max_connections gauge\n")
		fmt.Fprintf(rw, "dazyflow_pg_pool_max_connections %d\n", st.MaxConns())
		fmt.Fprint(rw, "# HELP dazyflow_pg_pool_empty_acquires_total Acquires that had to wait for a connection (cumulative).\n")
		fmt.Fprint(rw, "# TYPE dazyflow_pg_pool_empty_acquires_total counter\n")
		fmt.Fprintf(rw, "dazyflow_pg_pool_empty_acquires_total %d\n", st.EmptyAcquireCount())
	}

	// Session-lookup cache hit/miss — confirms the cache is absorbing the
	// per-request auth lookups, and the miss rate tracks raw auth load.
	if statter, ok := h.Sessions.(sessionCacheStatter); ok {
		hits, misses := statter.Stats()
		fmt.Fprint(rw, "# HELP dazyflow_session_cache_hits_total Session lookups served from the in-process cache (cumulative).\n")
		fmt.Fprint(rw, "# TYPE dazyflow_session_cache_hits_total counter\n")
		fmt.Fprintf(rw, "dazyflow_session_cache_hits_total %d\n", hits)
		fmt.Fprint(rw, "# HELP dazyflow_session_cache_misses_total Session lookups that fell through to the store (cumulative).\n")
		fmt.Fprint(rw, "# TYPE dazyflow_session_cache_misses_total counter\n")
		fmt.Fprintf(rw, "dazyflow_session_cache_misses_total %d\n", misses)
	}

	// HTTP RED + per-node latency histograms (cumulative, in-process).
	h.Metrics.render(rw)

	reporter, ok := h.quotaReporter()
	if !ok {
		return
	}
	usages := reporter.Usage()
	if len(usages) == 0 {
		return
	}
	fmt.Fprint(rw, "# HELP dazyflow_quota_bytes_used Sandbox bytes used by a tenant.\n")
	fmt.Fprint(rw, "# TYPE dazyflow_quota_bytes_used gauge\n")
	for _, u := range usages {
		fmt.Fprintf(rw, "dazyflow_quota_bytes_used{tenant=%s} %d\n", promLabel(u.Tenant), u.Used)
	}
	fmt.Fprint(rw, "# HELP dazyflow_quota_bytes_limit Tenant disk quota in bytes (0 = unlimited).\n")
	fmt.Fprint(rw, "# TYPE dazyflow_quota_bytes_limit gauge\n")
	for _, u := range usages {
		fmt.Fprintf(rw, "dazyflow_quota_bytes_limit{tenant=%s} %d\n", promLabel(u.Tenant), u.Limit)
	}
}

// quotaReporter returns the wired quota provider when it can enumerate
// per-tenant usage (FSQuota does; a bare provider may not).
func (h *metricsAPI) quotaReporter() (core.QuotaReporter, bool) {
	if h.svc == nil || h.svc.Engine == nil || h.svc.Engine.Quota == nil {
		return nil, false
	}
	r, ok := h.svc.Engine.Quota.(core.QuotaReporter)
	return r, ok
}

// promLabel renders a Prometheus label value with the escaping the
// exposition format requires (backslash, double-quote, newline), wrapped
// in double quotes. Tenant identifiers are already restricted to a safe
// charset, but escaping keeps the output well-formed regardless.
func promLabel(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
