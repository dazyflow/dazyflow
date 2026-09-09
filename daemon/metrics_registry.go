// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"fmt"
	"io"
	"maps"
	"math"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// Metrics is a tiny in-process metrics registry for the cumulative series the
// pull-on-scrape gauges in httpmetrics.go can't express: HTTP request
// rate/errors/duration and per-node execution latency. Hand-rolled to match the
// rest of /metrics, with no client_golang dependency.
//
// All observe paths are safe for concurrent callers — the hot increments are
// atomic and the mutex only guards lazy creation of a new label set, so after
// warmup it is contention-free. Cardinality is deliberately bounded and none of
// the label values are caller-controlled, so the series count stays small.
type Metrics struct {
	mu       sync.Mutex
	httpReqs map[string]*atomic.Int64 // key: method + "\x00" + statusCode
	httpDur  map[string]*histogram    // key: method
	nodeDur  map[string]*histogram    // key: terminal status
}

func NewMetrics() *Metrics {
	return &Metrics{
		httpReqs: map[string]*atomic.Int64{},
		httpDur:  map[string]*histogram{},
		nodeDur:  map[string]*histogram{},
	}
}

var durationBuckets = []float64{
	0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60,
}

func (m *Metrics) ObserveHTTP(method string, code int, seconds float64) {
	if m == nil {
		return
	}
	key := method + "\x00" + strconv.Itoa(code)
	m.mu.Lock()
	c := m.httpReqs[key]
	if c == nil {
		c = new(atomic.Int64)
		m.httpReqs[key] = c
	}
	d := m.httpDur[method]
	if d == nil {
		d = newHistogram(durationBuckets)
		m.httpDur[method] = d
	}
	m.mu.Unlock()
	c.Add(1)
	d.observe(seconds)
}

func (m *Metrics) ObserveNode(status string, seconds float64) {
	if m == nil {
		return
	}
	m.mu.Lock()
	d := m.nodeDur[status]
	if d == nil {
		d = newHistogram(durationBuckets)
		m.nodeDur[status] = d
	}
	m.mu.Unlock()
	d.observe(seconds)
}

// render writes the HTTP + node series in Prometheus text format. Called
// from the /metrics handler; takes the lock so the snapshot is
// self-consistent (scrapes are infrequent, so holding it is fine).
func (m *Metrics) render(w io.Writer) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(m.httpReqs) > 0 {
		fmt.Fprint(w, "# HELP dazyflow_http_requests_total HTTP requests handled, by method and status code.\n")
		fmt.Fprint(w, "# TYPE dazyflow_http_requests_total counter\n")
		keys := make([]string, 0, len(m.httpReqs))
		for k := range m.httpReqs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			method, code, _ := strings.Cut(k, "\x00")
			fmt.Fprintf(w, "dazyflow_http_requests_total{method=%s,code=%s} %d\n",
				promLabel(method), promLabel(code), m.httpReqs[k].Load())
		}
	}

	if len(m.httpDur) > 0 {
		fmt.Fprint(w, "# HELP dazyflow_http_request_duration_seconds HTTP request latency by method.\n")
		fmt.Fprint(w, "# TYPE dazyflow_http_request_duration_seconds histogram\n")
		for _, method := range slices.Sorted(maps.Keys(m.httpDur)) {
			m.httpDur[method].render(w, "dazyflow_http_request_duration_seconds",
				"method="+promLabel(method))
		}
	}

	if len(m.nodeDur) > 0 {
		fmt.Fprint(w, "# HELP dazyflow_node_duration_seconds Per-node execution latency by terminal status.\n")
		fmt.Fprint(w, "# TYPE dazyflow_node_duration_seconds histogram\n")
		for _, status := range slices.Sorted(maps.Keys(m.nodeDur)) {
			m.nodeDur[status].render(w, "dazyflow_node_duration_seconds",
				"status="+promLabel(status))
		}
	}
}

// histogram is a fixed-bucket cumulative histogram. Bucket counts and
// the count are atomic; the running sum uses a CAS loop over float64
// bits so the whole observe path is lock-free.
type histogram struct {
	bounds  []float64      // ascending upper bounds (exclusive of +Inf)
	counts  []atomic.Int64 // per-bucket, len = len(bounds)+1 (last = +Inf)
	sumBits atomic.Uint64  // float64 bits of the running sum
	total   atomic.Int64
}

func newHistogram(bounds []float64) *histogram {
	return &histogram{
		bounds: bounds,
		counts: make([]atomic.Int64, len(bounds)+1),
	}
}

func (h *histogram) observe(v float64) {
	// Smallest bucket whose upper bound is >= v (Prometheus le is
	// inclusive); len(bounds) when v exceeds every bound (+Inf bucket).
	i := sort.SearchFloat64s(h.bounds, v)
	h.counts[i].Add(1)
	h.total.Add(1)
	for {
		old := h.sumBits.Load()
		next := math.Float64bits(math.Float64frombits(old) + v)
		if h.sumBits.CompareAndSwap(old, next) {
			break
		}
	}
}

func (h *histogram) render(w io.Writer, name, labels string) {
	cum := int64(0)
	for i, b := range h.bounds {
		cum += h.counts[i].Load()
		fmt.Fprintf(w, "%s_bucket{%s,le=%s} %d\n", name, labels, promLabel(strconv.FormatFloat(b, 'g', -1, 64)), cum)
	}
	cum += h.counts[len(h.bounds)].Load()
	fmt.Fprintf(w, "%s_bucket{%s,le=%s} %d\n", name, labels, promLabel("+Inf"), cum)
	fmt.Fprintf(w, "%s_sum{%s} %g\n", name, labels, math.Float64frombits(h.sumBits.Load()))
	fmt.Fprintf(w, "%s_count{%s} %d\n", name, labels, h.total.Load())
}

type statusRecorder struct {
	http.ResponseWriter
	code  int
	wrote bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wrote {
		s.code = code
		s.wrote = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.wrote {
		s.code = http.StatusOK
		s.wrote = true
	}
	return s.ResponseWriter.Write(b)
}

func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

func (s *statusRecorder) statusCode() int {
	if s.code == 0 {
		return http.StatusOK
	}
	return s.code
}
