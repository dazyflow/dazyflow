// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func usageBody(t *testing.T, rw *httptest.ResponseRecorder) []UsageCounters {
	t.Helper()
	var out struct {
		Usage []UsageCounters `json:"usage"`
	}
	if err := json.Unmarshal(rw.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rw.Body.String())
	}
	return out.Usage
}

func TestUsageMe(t *testing.T) {
	h := newGatewayHarness(t)
	store := NewMemUsageStore()
	h.svc.Usage = store
	now := time.Now()
	_ = store.AddRun(t.Context(), "t", now)
	_ = store.AddNodeExecutions(t.Context(), "t", 5, now)
	_ = store.AddRun(t.Context(), "t", now.AddDate(0, -1, 0))
	_ = store.AddRun(t.Context(), "other-tenant", now)

	rw := h.do(t, "GET", "/api/v1/me/usage", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rw.Code, rw.Body.String())
	}
	usage := usageBody(t, rw)
	if len(usage) != 2 {
		t.Fatalf("got %d buckets, want 2: %+v", len(usage), usage)
	}
	// Newest first; only the principal's tenant ("t") is visible.
	if usage[0].Period != usagePeriod(now) || usage[0].GraphRuns != 1 || usage[0].NodeExecutions != 5 {
		t.Errorf("current = %+v, want %s/1/5", usage[0], usagePeriod(now))
	}
	if usage[1].GraphRuns != 1 || usage[1].NodeExecutions != 0 {
		t.Errorf("previous = %+v, want 1/0", usage[1])
	}
}

func TestUsageMe_EmptyTenantSynthesizesCurrentMonth(t *testing.T) {
	h := newGatewayHarness(t)
	h.svc.Usage = NewMemUsageStore()

	rw := h.do(t, "GET", "/api/v1/me/usage", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rw.Code, rw.Body.String())
	}
	usage := usageBody(t, rw)
	if len(usage) != 1 || usage[0].Period != usagePeriod(time.Now()) ||
		usage[0].GraphRuns != 0 || usage[0].NodeExecutions != 0 {
		t.Errorf("got %+v, want a single zeroed current-month bucket", usage)
	}
}

func TestUsageMe_CrossTenantForbidden(t *testing.T) {
	h := newGatewayHarness(t)
	h.svc.Usage = NewMemUsageStore()

	rw := h.do(t, "GET", "/api/v1/me/usage?tenant=other-tenant", nil)
	if rw.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body %s)", rw.Code, rw.Body.String())
	}
}

func TestUsageMe_NotConfigured(t *testing.T) {
	h := newGatewayHarness(t)

	rw := h.do(t, "GET", "/api/v1/me/usage", nil)
	if rw.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501 (body %s)", rw.Code, rw.Body.String())
	}
}

func TestUsageMe_RequiresAuth(t *testing.T) {
	h := newGatewayHarness(t)
	h.svc.Usage = NewMemUsageStore()

	req := httptest.NewRequest("GET", "/api/v1/me/usage", nil)
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	if rw.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rw.Code)
	}
}
