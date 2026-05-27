package daemon

import (
	"encoding/json"
	"net/http"
	"testing"
)

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
