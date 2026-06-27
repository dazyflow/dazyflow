// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"net/http"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/auth"
	"git.sr.ht/~klahr/dazyflow/core"
)

// HTTP-handler authz / scope / not-configured branches of the /me/boards
// endpoints. The default harness has no Engine.Sandbox, so the board service
// reports errBoardsUnavailable -> 501.

func TestListBoardsMe_NotConfigured(t *testing.T) {
	h := newGatewayHarness(t)
	rw := h.do(t, "GET", "/api/v1/me/boards", nil)
	if rw.Code != http.StatusNotImplemented {
		t.Fatalf("list boards no sandbox = %d (%s), want 501", rw.Code, rw.Body.String())
	}
	if got := rw.Body.String(); !strings.Contains(got, "not_configured") {
		t.Errorf("body %s", got)
	}
}

func TestGetBoardMe_NotConfigured(t *testing.T) {
	h := newGatewayHarness(t)
	rw := h.do(t, "GET", "/api/v1/me/boards/leads", nil)
	if rw.Code != http.StatusNotImplemented {
		t.Fatalf("get board no sandbox = %d (%s), want 501", rw.Code, rw.Body.String())
	}
}

func TestListBoardsMe_ForbiddenScope(t *testing.T) {
	h := newGatewayHarness(t)
	// Cross-tenant ?tenant= -> 403 forbidden_scope.
	rw := h.do(t, "GET", "/api/v1/me/boards?tenant=other", nil)
	if rw.Code != http.StatusForbidden {
		t.Fatalf("cross-tenant boards = %d (%s), want 403", rw.Code, rw.Body.String())
	}
	// Cross-workspace.
	rw = h.do(t, "GET", "/api/v1/me/boards?workspace=other", nil)
	if rw.Code != http.StatusForbidden {
		t.Fatalf("cross-workspace boards = %d, want 403", rw.Code)
	}
}

func TestListBoardsMe_MissingScope(t *testing.T) {
	h := newGatewayHarness(t)
	// A token with no tenant/workspace binding and no query params -> 400.
	role := core.Role{Name: "free", Permissions: []core.Permission{core.PermGraphRun}}
	_, tok, err := auth.IssueAPIKey(h.ks, t.Context(), "k-unbound", "", "", "nobody", []core.Role{role}, nil)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	saved := h.token
	h.token = tok
	defer func() { h.token = saved }()
	rw := h.do(t, "GET", "/api/v1/me/boards", nil)
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("unbound boards = %d (%s), want 400", rw.Code, rw.Body.String())
	}
}

func TestClearBoardMe_ForbiddenWithoutEditPerm(t *testing.T) {
	h := newRunOnlyHarness(t)
	// Run-only token lacks graph:edit; clear is 403.
	rw := runOnlyDo(t, h, "DELETE", "/api/v1/me/boards/leads", nil)
	if rw.Code != http.StatusForbidden {
		t.Fatalf("run-only clear = %d (%s), want 403", rw.Code, rw.Body.String())
	}
}
