// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"strings"
	"testing"
)

func TestHistogram_BucketsCumulativeAndSum(t *testing.T) {
	h := newHistogram([]float64{0.1, 1, 10})
	for _, v := range []float64{0.05, 0.5, 5, 50} {
		h.observe(v)
	}
	var b strings.Builder
	h.render(&b, "test_seconds", `k="v"`)
	out := b.String()

	// Cumulative bucket counts: <=0.1 →1, <=1 →2, <=10 →3, +Inf →4.
	for _, want := range []string{
		`test_seconds_bucket{k="v",le="0.1"} 1`,
		`test_seconds_bucket{k="v",le="1"} 2`,
		`test_seconds_bucket{k="v",le="10"} 3`,
		`test_seconds_bucket{k="v",le="+Inf"} 4`,
		`test_seconds_count{k="v"} 4`,
		`test_seconds_sum{k="v"} 55.55`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("histogram render missing %q\n--- out ---\n%s", want, out)
		}
	}
}

func TestMetrics_RenderHTTPAndNode(t *testing.T) {
	m := NewMetrics()
	m.ObserveHTTP("GET", 200, 0.02)
	m.ObserveHTTP("GET", 200, 0.03)
	m.ObserveHTTP("POST", 500, 1.5)
	m.ObserveNode("succeeded", 0.4)
	m.ObserveNode("failed", 12)

	var b strings.Builder
	m.render(&b)
	out := b.String()

	for _, want := range []string{
		`dazyflow_http_requests_total{method="GET",code="200"} 2`,
		`dazyflow_http_requests_total{method="POST",code="500"} 1`,
		`dazyflow_http_request_duration_seconds_count{method="GET"} 2`,
		`dazyflow_http_request_duration_seconds_count{method="POST"} 1`,
		`dazyflow_node_duration_seconds_count{status="succeeded"} 1`,
		`dazyflow_node_duration_seconds_count{status="failed"} 1`,
		`dazyflow_node_duration_seconds_bucket{status="failed",le="10"} 0`,
		`dazyflow_node_duration_seconds_bucket{status="failed",le="30"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("metrics render missing %q\n--- out ---\n%s", want, out)
		}
	}
}

func TestMetrics_NilSafe(t *testing.T) {
	var m *Metrics
	// All paths must no-op on a nil registry rather than panic.
	m.ObserveHTTP("GET", 200, 0.1)
	m.ObserveNode("succeeded", 0.1)
	var b strings.Builder
	m.render(&b)
	if b.Len() != 0 {
		t.Errorf("nil registry rendered %q, want empty", b.String())
	}
}
