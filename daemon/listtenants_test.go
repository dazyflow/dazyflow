// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestListTenants_Cov(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)

	if rw := h.do(t, "GET", "/api/v1/admin/tenants", nil); rw.Code != http.StatusForbidden {
		t.Fatalf("non-platform = %d, want 403; body=%s", rw.Code, rw.Body.String())
	}

	rw := h.platformDo(t, "GET", "/api/v1/admin/tenants", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("platform = %d, want 200; body=%s", rw.Code, rw.Body.String())
	}
	var resp struct {
		Tenants []string `json:"tenants"`
	}
	_ = json.Unmarshal(rw.Body.Bytes(), &resp)
	found := false
	for _, tn := range resp.Tenants {
		if tn == "t" {
			found = true
		}
	}
	if !found {
		t.Fatalf("tenants = %v, want to include 't'", resp.Tenants)
	}
}
