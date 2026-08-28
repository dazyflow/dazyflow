// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"net/http"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/auth"
	"git.sr.ht/~klahr/dazyflow/core"
)

// unboundToken issues an API key with no tenant/workspace binding so the
// /me listings hit their missing_scope branch.
func unboundToken(t *testing.T, h *gatewayHarness) string {
	t.Helper()
	role := core.Role{Name: "free", Permissions: []core.Permission{core.PermGraphRun}}
	_, tok, err := auth.IssueAPIKey(h.ks, t.Context(), "k-unbound-list", "", "", "nobody", []core.Role{role}, nil)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	return tok
}

func TestListFlowsMe_MissingScope(t *testing.T) {
	h := newGatewayHarness(t)
	tok := unboundToken(t, h)
	saved := h.token
	h.token = tok
	defer func() { h.token = saved }()
	rw := h.do(t, "GET", "/api/v1/me/flows", nil)
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("unbound flows = %d (%s), want 400", rw.Code, rw.Body.String())
	}
}

func TestListFlowsMe_OK(t *testing.T) {
	h := newGatewayHarness(t)
	covSeedFlow(t, h, "f1")
	rw := h.do(t, "GET", "/api/v1/me/flows", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("list flows = %d (%s), want 200", rw.Code, rw.Body.String())
	}
}

func TestSuggestionsMe_MissingScope(t *testing.T) {
	h := newGatewayHarness(t)
	tok := unboundToken(t, h)
	saved := h.token
	h.token = tok
	defer func() { h.token = saved }()
	rw := h.do(t, "GET", "/api/v1/me/flows/suggestions", nil)
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("unbound suggestions = %d (%s), want 400", rw.Code, rw.Body.String())
	}
}

func TestSuggestionsMe_OK(t *testing.T) {
	h := newGatewayHarness(t)
	covSeedFlow(t, h, "f1")
	rw := h.do(t, "GET", "/api/v1/me/flows/suggestions", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("suggestions = %d (%s), want 200", rw.Code, rw.Body.String())
	}
}

// TestResolveTenantWorkspaceScope_ForbiddenScope pins the cross-scope guard on
// every surface that resolves ?tenant=/?workspace= through
// resolveTenantWorkspaceScope. The harness principal is bound to t/ws, so
// naming any other tenant or workspace must be refused at the handler with a
// 403 — not passed down for the service layer to reject. The service methods
// behind these routes do re-check with core.RequireWorkspace; this test is what
// keeps the boundary guard from being deleted as redundant, so a future handler
// reaching a store directly can't silently inherit a cross-tenant read.
func TestResolveTenantWorkspaceScope_ForbiddenScope(t *testing.T) {
	paths := []string{
		"/api/v1/me/flows",
		"/api/v1/me/flows/suggestions",
		"/api/v1/me/share",
	}
	for _, p := range paths {
		for _, q := range []string{"?tenant=other", "?workspace=other"} {
			t.Run(p+q, func(t *testing.T) {
				h := newGatewayHarness(t)
				rw := h.do(t, "GET", p+q, nil)
				if rw.Code != http.StatusForbidden {
					t.Fatalf("GET %s%s = %d (%s), want 403", p, q, rw.Code, rw.Body.String())
				}
				if got := rw.Body.String(); !strings.Contains(got, "forbidden_scope") {
					t.Errorf("body %s, want forbidden_scope", got)
				}
			})
		}
	}
}

// A platform admin carries no tenant binding, so the guard must not fence it
// out of another tenant's listing — that cross-tenant reach is the role's whole
// purpose (see core.RequireTenant). Any non-403 proves the escape hatch
// survived; the exact code depends on what the harness store holds.
func TestResolveTenantWorkspaceScope_PlatformAdminCrossesTenant(t *testing.T) {
	h := newGatewayHarness(t)
	rw := h.platformDo(t, "GET", "/api/v1/me/flows?tenant=other&workspace=ws", nil)
	if rw.Code == http.StatusForbidden {
		t.Fatalf("platform admin cross-tenant flows = 403 (%s), want the guard to let it through", rw.Body.String())
	}
}

func TestHistoryFlowMe_OKWithLimit(t *testing.T) {
	h := newGatewayHarness(t)
	covSeedFlow(t, h, "f1")
	rw := h.do(t, "GET", "/api/v1/me/flows/"+cov3FlowID+"/history?limit=5", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("history with limit = %d (%s), want 200", rw.Code, rw.Body.String())
	}
}

func TestHistoryFlowMe_BadLimitFallsBack(t *testing.T) {
	h := newGatewayHarness(t)
	covSeedFlow(t, h, "f1")
	// Non-numeric limit is ignored (falls back to default), still 200.
	rw := h.do(t, "GET", "/api/v1/me/flows/"+cov3FlowID+"/history?limit=abc", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("history bad limit = %d (%s), want 200", rw.Code, rw.Body.String())
	}
}
