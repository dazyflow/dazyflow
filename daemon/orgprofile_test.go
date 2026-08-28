// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/auth"
)

func TestOrgProfile_GetPut_Cov(t *testing.T) {
	h := newGatewayHarness(t)
	prof := newCovProfiles()
	h.gw.Profiles = prof
	h.gw.WildcardDomain = "dazyflow.app"
	ctx := context.Background()

	// GET with no row -> default shape.
	rw := teamAdminDo(t, h, "GET", "/api/v1/admin/org/profile", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("get default = %d: %s", rw.Code, rw.Body.String())
	}
	var gr struct {
		DisplayName    string `json:"display_name"`
		WildcardDomain string `json:"wildcard_domain"`
	}
	_ = json.Unmarshal(rw.Body.Bytes(), &gr)
	if gr.DisplayName != "" || gr.WildcardDomain != "dazyflow.app" {
		t.Fatalf("default profile = %+v", gr)
	}

	// PUT name + icon.
	rw = teamAdminDo(t, h, "PUT", "/api/v1/admin/org/profile", map[string]any{
		"display_name": "  Acme  ", "icon": "data:image/png;base64,AAAA",
	})
	if rw.Code != http.StatusOK {
		t.Fatalf("put = %d: %s", rw.Code, rw.Body.String())
	}
	if p, _ := prof.GetOrgProfile(ctx, "t"); p.DisplayName != "Acme" {
		t.Fatalf("stored name = %q, want trimmed Acme", p.DisplayName)
	}

	// GET now returns the populated profile.
	rw = teamAdminDo(t, h, "GET", "/api/v1/admin/org/profile", nil)
	_ = json.Unmarshal(rw.Body.Bytes(), &gr)
	if gr.DisplayName != "Acme" {
		t.Fatalf("get populated name = %q", gr.DisplayName)
	}

	// Name too long -> 400.
	if rw := teamAdminDo(t, h, "PUT", "/api/v1/admin/org/profile", map[string]any{
		"display_name": strings.Repeat("x", 81),
	}); rw.Code != http.StatusBadRequest {
		t.Fatalf("long name = %d, want 400", rw.Code)
	}

	// Icon too large -> 400.
	if rw := teamAdminDo(t, h, "PUT", "/api/v1/admin/org/profile", map[string]any{
		"icon": strings.Repeat("x", maxOrgIconBytes+1),
	}); rw.Code != http.StatusBadRequest {
		t.Fatalf("large icon = %d, want 400", rw.Code)
	}

	// PUT preserves an existing subdomain across a name save.
	_ = prof.PutOrgProfile(ctx, auth.OrgProfile{Tenant: "t", DisplayName: "Acme", Subdomain: "acme"})
	if rw := teamAdminDo(t, h, "PUT", "/api/v1/admin/org/profile", map[string]any{"display_name": "Acme2"}); rw.Code != http.StatusOK {
		t.Fatalf("put preserve = %d", rw.Code)
	}
	if p, _ := prof.GetOrgProfile(ctx, "t"); p.Subdomain != "acme" {
		t.Fatalf("subdomain not preserved: %q", p.Subdomain)
	}
}

func TestOrgProfile_NotConfiguredAndAuthz_Cov(t *testing.T) {
	// Nil store -> 501.
	h := newGatewayHarness(t)
	if rw := teamAdminDo(t, h, "GET", "/api/v1/admin/org/profile", nil); rw.Code != http.StatusNotImplemented {
		t.Fatalf("nil store get = %d, want 501", rw.Code)
	}

	// Non-admin (editor token) -> 403.
	h2 := newGatewayHarness(t)
	h2.gw.Profiles = newCovProfiles()
	if rw := h2.do(t, "GET", "/api/v1/admin/org/profile", nil); rw.Code != http.StatusForbidden {
		t.Fatalf("editor get = %d, want 403", rw.Code)
	}

	// Cross-tenant view forbidden.
	if rw := teamAdminDo(t, h2, "GET", "/api/v1/admin/org/profile?tenant=other", nil); rw.Code != http.StatusForbidden {
		t.Fatalf("cross-tenant = %d, want 403", rw.Code)
	}
}
