package daemon

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestLimitRequestBody_RejectsOversizedContentLength verifies the global body
// guard rejects a POST whose declared Content-Length exceeds the ceiling
// before the (tiny) body is ever read — the early-allocation guard. It fires
// pre-routing/pre-auth, so any POST path exercises it.
func TestLimitRequestBody_RejectsOversizedContentLength(t *testing.T) {
	h := newGatewayHarness(t)
	req := httptest.NewRequest("POST", "/api/v1/flows", bytes.NewBufferString("{}"))
	req.Header.Set("Authorization", "Bearer "+h.token)
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = maxRequestBody + 1 // claim more than the ceiling allows
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	if rw.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413 for oversized Content-Length", rw.Code)
	}
}

// A normal-sized POST is unaffected by the guard (reaches routing/auth).
func TestLimitRequestBody_AllowsNormalBody(t *testing.T) {
	h := newGatewayHarness(t)
	rw := h.do(t, "POST", "/api/v1/flows", map[string]any{"id": "x"})
	if rw.Code == http.StatusRequestEntityTooLarge {
		t.Fatalf("normal body got 413; guard is too aggressive")
	}
}

func TestWorkspaceLimits_AdminOnly(t *testing.T) {
	h := newGatewayHarness(t)
	if rw := h.do(t, "GET", "/api/v1/admin/limits", nil); rw.Code != http.StatusForbidden {
		t.Fatalf("editor status = %d, want 403", rw.Code)
	}
	if rw := h.adminDo(t, "GET", "/api/v1/admin/limits", nil); rw.Code != http.StatusOK {
		t.Fatalf("admin status = %d, want 200", rw.Code)
	}
}

func TestWorkspaceLimits_ReportsValues(t *testing.T) {
	h := newGatewayHarness(t)
	h.svc.MaxGraphNodes = 50
	q, _ := NewFSQuota(t.TempDir(), map[string]int64{"t": 1000})
	q.SetCacheTTL(0)
	h.svc.Engine.Quota = q

	rw := h.adminDo(t, "GET", "/api/v1/admin/limits", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d", rw.Code)
	}
	var got struct {
		Tenant        string `json:"tenant"`
		MaxGraphNodes int    `json:"max_graph_nodes"`
		Quota         struct {
			UsedBytes  int64 `json:"used_bytes"`
			LimitBytes int64 `json:"limit_bytes"`
		} `json:"quota"`
	}
	if err := json.Unmarshal(rw.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Tenant != "t" || got.MaxGraphNodes != 50 {
		t.Errorf("tenant=%q max_graph_nodes=%d, want t/50", got.Tenant, got.MaxGraphNodes)
	}
	if got.Quota.LimitBytes != 1000 {
		t.Errorf("quota limit = %d, want 1000", got.Quota.LimitBytes)
	}
}
