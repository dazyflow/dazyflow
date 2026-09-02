// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
)

func TestMetrics_JobGauges(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)
	h.gw.EnableMetrics = true

	// Two queued node jobs → queue-depth gauge of 2; running stays 0.
	for _, id := range []string{"j1", "j2"} {
		if err := h.store.Enqueue(t.Context(), core.JobRecord{ID: id, Kind: core.JobKindNode, Tenant: "t"}); err != nil {
			t.Fatalf("enqueue %s: %v", id, err)
		}
	}
	rw := h.do(t, "GET", "/metrics", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d", rw.Code)
	}
	body := rw.Body.String()
	for _, want := range []string{
		`dazyflow_jobs{status="queued"} 2`,
		`dazyflow_jobs{status="running"} 0`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics body missing %q\n--- body ---\n%s", want, body)
		}
	}
}

func TestMetrics_SessionCacheGauges(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)
	h.gw.EnableMetrics = true

	// Wrap a session store with the cache and prime one hit + one miss.
	cache := auth.NewCachingSessionStore(auth.NewMemSessionStore(), time.Minute, 0)
	sess := auth.Session{ID: "s1", Subject: "u", Tenant: "t", ExpiresAt: time.Now().Add(time.Hour)}
	if err := cache.PutSession(t.Context(), sess); err != nil {
		t.Fatalf("put: %v", err)
	}
	_, _ = cache.GetSession(t.Context(), "s1")      // hit
	_, _ = cache.GetSession(t.Context(), "missing") // miss
	h.gw.Sessions = cache

	body := h.do(t, "GET", "/metrics", nil).Body.String()
	for _, want := range []string{
		"dazyflow_session_cache_hits_total 1",
		"dazyflow_session_cache_misses_total 1",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics body missing %q\n--- body ---\n%s", want, body)
		}
	}
}

func TestMetrics_HTTPRedSeries(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)
	h.gw.EnableMetrics = true
	h.gw.Metrics = NewMetrics()

	// Drive a couple of requests through the full middleware chain so the
	// RED counters + duration histogram accumulate.
	h.do(t, "GET", "/healthz", nil)
	h.do(t, "GET", "/healthz", nil)

	body := h.do(t, "GET", "/metrics", nil).Body.String()
	for _, want := range []string{
		`dazyflow_http_requests_total{method="GET",code="200"}`,
		"dazyflow_http_request_duration_seconds_bucket",
		`dazyflow_http_request_duration_seconds_count{method="GET"}`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics body missing %q\n--- body ---\n%s", want, body)
		}
	}
}

func TestMetrics_DisabledByDefault(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t) // EnableMetrics defaults false
	rw := h.do(t, "GET", "/metrics", nil)
	if rw.Code != http.StatusNotFound {
		t.Fatalf("GET /metrics with metrics disabled = %d, want 404 (route not mounted)", rw.Code)
	}
}

func TestMetrics_EnabledEmitsUpAndQuotaGauges(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)
	h.gw.EnableMetrics = true

	// Wire a quota provider with a limited tenant and seed some usage.
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "acme", "ws"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "acme", "ws", "f"), make([]byte, 200), 0o644); err != nil {
		t.Fatal(err)
	}
	q, err := NewFSQuota(base, map[string]int64{"acme": 1000})
	if err != nil {
		t.Fatal(err)
	}
	q.SetCacheTTL(0)
	h.svc.Engine.Quota = q

	rw := h.do(t, "GET", "/metrics", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rw.Code)
	}
	if ct := rw.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want Prometheus text", ct)
	}
	body := rw.Body.String()
	for _, want := range []string{
		"dazyflow_up 1",
		`dazyflow_quota_bytes_used{tenant="acme"} 200`,
		`dazyflow_quota_bytes_limit{tenant="acme"} 1000`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics body missing %q\n--- body ---\n%s", want, body)
		}
	}
}

func TestMetrics_EnabledWithoutQuotaStillServesUp(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)
	h.gw.EnableMetrics = true // no quota provider wired on the harness engine
	rw := h.do(t, "GET", "/metrics", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rw.Code)
	}
	body := rw.Body.String()
	if !strings.Contains(body, "dazyflow_up 1") {
		t.Errorf("missing dazyflow_up gauge: %s", body)
	}
	if strings.Contains(body, "dazyflow_quota_bytes_used") {
		t.Errorf("quota gauges present without a quota provider: %s", body)
	}
}
