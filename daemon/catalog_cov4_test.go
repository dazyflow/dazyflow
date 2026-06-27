package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

// TestListIntegrationsHandler_Cov covers listIntegrationsHandler across the
// unfiltered, query-filtered, and category-filtered branches.
func TestListIntegrationsHandler_Cov(t *testing.T) {
	h := newGatewayHarness(t)

	// Unfiltered -> 200 with items.
	rw := h.do(t, "GET", "/api/v1/catalog/integrations", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("list = %d, want 200; body=%s", rw.Code, rw.Body.String())
	}
	var resp struct {
		Items []map[string]any `json:"items"`
	}
	_ = json.Unmarshal(rw.Body.Bytes(), &resp)
	if len(resp.Items) == 0 {
		t.Fatalf("unfiltered integrations empty: %s", rw.Body.String())
	}

	// A query that matches nothing returns an empty (but valid) list.
	rw = h.do(t, "GET", "/api/v1/catalog/integrations?q=zzz_no_such_integration", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("q-filtered = %d, want 200", rw.Code)
	}
	resp.Items = nil
	_ = json.Unmarshal(rw.Body.Bytes(), &resp)
	if len(resp.Items) != 0 {
		t.Fatalf("nonsense query matched %d items", len(resp.Items))
	}

	// A category filter exercises the dropCategories branch.
	rw = h.do(t, "GET", "/api/v1/catalog/integrations?category=flow_control", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("category-filtered = %d, want 200", rw.Code)
	}
}

// TestRequireSecretStore_Cov covers requireSecretStore's three legs.
func TestRequireSecretStore_Cov(t *testing.T) {
	h := newGatewayHarness(t)

	// No store -> 501, returns false.
	rw := httptest.NewRecorder()
	if h.gw.requireSecretStore(rw, core.Principal{Tenant: "t"}) {
		t.Fatal("no-store should return false")
	}
	if rw.Code != http.StatusNotImplemented {
		t.Fatalf("no-store = %d, want 501", rw.Code)
	}

	h.gw.EncryptedSecrets = testEncryptedSecrets(t)

	// Store present but no tenant -> 403, false.
	rw = httptest.NewRecorder()
	if h.gw.requireSecretStore(rw, core.Principal{}) {
		t.Fatal("no-tenant should return false")
	}
	if rw.Code != http.StatusForbidden {
		t.Fatalf("no-tenant = %d, want 403", rw.Code)
	}

	// Store + tenant -> true, no write.
	rw = httptest.NewRecorder()
	if !h.gw.requireSecretStore(rw, core.Principal{Tenant: "t"}) {
		t.Fatal("store+tenant should return true")
	}
	if rw.Code != http.StatusOK {
		t.Fatalf("store+tenant wrote a status: %d", rw.Code)
	}
}
