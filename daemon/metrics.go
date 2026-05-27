package daemon

import (
	"fmt"
	"net/http"
	"strings"

	"git.sr.ht/~klahr/hazy-flow/core"
)

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
func (h *HTTPGateway) metrics(rw http.ResponseWriter, r *http.Request) {
	rw.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

	fmt.Fprint(rw, "# HELP hazyflow_up 1 when the daemon is serving.\n")
	fmt.Fprint(rw, "# TYPE hazyflow_up gauge\n")
	fmt.Fprint(rw, "hazyflow_up 1\n")

	// Node-job counts by status — queue depth (queued) + in-flight
	// (running) are the load-bearing signals.
	if counter, ok := h.svc.Jobs.(core.JobCounter); ok {
		if counts, err := counter.CountsByStatus(r.Context()); err == nil {
			fmt.Fprint(rw, "# HELP hazyflow_jobs Node-job records currently in the store, by status.\n")
			fmt.Fprint(rw, "# TYPE hazyflow_jobs gauge\n")
			for _, status := range jobStatusOrder {
				fmt.Fprintf(rw, "hazyflow_jobs{status=%s} %d\n", promLabel(string(status)), counts[status])
			}
		}
	}

	reporter, ok := h.quotaReporter()
	if !ok {
		return
	}
	usages := reporter.Usage()
	if len(usages) == 0 {
		return
	}
	fmt.Fprint(rw, "# HELP hazyflow_quota_bytes_used Sandbox bytes used by a tenant.\n")
	fmt.Fprint(rw, "# TYPE hazyflow_quota_bytes_used gauge\n")
	for _, u := range usages {
		fmt.Fprintf(rw, "hazyflow_quota_bytes_used{tenant=%s} %d\n", promLabel(u.Tenant), u.Used)
	}
	fmt.Fprint(rw, "# HELP hazyflow_quota_bytes_limit Tenant disk quota in bytes (0 = unlimited).\n")
	fmt.Fprint(rw, "# TYPE hazyflow_quota_bytes_limit gauge\n")
	for _, u := range usages {
		fmt.Fprintf(rw, "hazyflow_quota_bytes_limit{tenant=%s} %d\n", promLabel(u.Tenant), u.Limit)
	}
}

// quotaReporter returns the wired quota provider when it can enumerate
// per-tenant usage (FSQuota does; a bare provider may not).
func (h *HTTPGateway) quotaReporter() (core.QuotaReporter, bool) {
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
