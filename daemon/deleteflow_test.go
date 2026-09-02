// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
)

// deleteFlowMe gates deletion differently per credential kind: a session
// re-supplies its account password (it's ambient and hijackable), while an API
// key — which has no password — must carry graph:admin. These cover both
// paths; the gateway harness's own token is an API key, so the password cases
// need a session credential wired explicitly.

// deleteFlowSessionHarness wires Users + Sessions + a session authenticator and
// returns a signed-in session token for an editor in t/ws.
func deleteFlowSessionHarness(t *testing.T, password string) (*gatewayHarness, string) {
	t.Helper()
	h := newGatewayHarness(t)
	users, _ := auth.OpenJSONUserStore("")
	user := auth.User{
		Email: "alice", Subject: "alice", Tenant: "t", Workspace: "ws",
		Roles: []core.Role{core.TeamRoleEditor()},
	}
	if password != "" {
		hash, _ := auth.HashPassword(password)
		user.PasswordHash = hash
	}
	_ = users.PutUser(t.Context(), user)
	sessions := auth.NewMemSessionStore()
	h.gw.Users = users
	h.gw.Sessions = sessions
	h.svc.Auth = auth.Chain{
		&auth.APIKeyAuthenticator{Store: h.ks},
		&auth.SessionAuthenticator{Store: sessions},
	}
	_, tok, err := auth.IssueSession(t.Context(), sessions, user, time.Hour)
	if err != nil {
		t.Fatalf("issue session: %v", err)
	}
	return h, tok
}

func TestDeleteFlowMe_SessionNotConfigured(t *testing.T) {
	t.Parallel()
	h, tok := deleteFlowSessionHarness(t, "correct-pw")
	h.gw.Users = nil // no password store => the session gate can't be evaluated
	covSeedFlow(t, h, "f1")
	rw := sessionDo(t, h, tok, "DELETE", "/api/v1/me/flows/"+cov3FlowID, map[string]any{"password": "x"})
	if rw.Code != http.StatusNotImplemented {
		t.Fatalf("delete flow no Users = %d (%s), want 501", rw.Code, rw.Body.String())
	}
}

func TestDeleteFlowMe_SessionBadPassword(t *testing.T) {
	t.Parallel()
	h, tok := deleteFlowSessionHarness(t, "correct-pw")
	covSeedFlow(t, h, "f1")
	rw := sessionDo(t, h, tok, "DELETE", "/api/v1/me/flows/"+cov3FlowID, map[string]any{"password": "wrong"})
	if rw.Code != http.StatusUnauthorized {
		t.Fatalf("delete flow bad pw = %d (%s), want 401", rw.Code, rw.Body.String())
	}
	if _, err := h.ws.Load("f1"); err != nil {
		t.Fatal("flow was deleted despite the wrong password")
	}
}

func TestDeleteFlowMe_SessionOK(t *testing.T) {
	t.Parallel()
	h, tok := deleteFlowSessionHarness(t, "correct-pw")
	covSeedFlow(t, h, "f1")
	rw := sessionDo(t, h, tok, "DELETE", "/api/v1/me/flows/"+cov3FlowID, map[string]any{"password": "correct-pw"})
	if rw.Code != http.StatusNoContent {
		t.Fatalf("delete flow = %d (%s), want 204", rw.Code, rw.Body.String())
	}
	// Idempotent: deleting again is a no-op success (the flow is gone).
	rw = sessionDo(t, h, tok, "DELETE", "/api/v1/me/flows/"+cov3FlowID, map[string]any{"password": "correct-pw"})
	if rw.Code != http.StatusNoContent {
		t.Fatalf("re-delete flow = %d (%s), want 204", rw.Code, rw.Body.String())
	}
}

// An API key holding graph:admin deletes with no password in the body — the
// case that used to be impossible (it answered 401 for every key), which is
// why the MCP delete_flow tool could never succeed.
func TestDeleteFlowMe_APIKeyWithAdminScope(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t) // harness token is an editor key: graph:admin included
	covSeedFlow(t, h, "f1")
	rw := h.do(t, "DELETE", "/api/v1/me/flows/"+cov3FlowID, nil)
	if rw.Code != http.StatusNoContent {
		t.Fatalf("key delete = %d (%s), want 204", rw.Code, rw.Body.String())
	}
	if _, err := h.ws.Load("f1"); err == nil {
		t.Fatal("flow still present after a successful delete")
	}
}

// A key with the narrow `claude-mcp` scope set (graph:run + graph:edit, no
// graph:admin) may author and run flows but must not destroy one's history.
func TestDeleteFlowMe_APIKeyWithoutAdminScope(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)
	role := core.Role{Name: "claude-mcp", Permissions: []core.Permission{
		core.PermGraphRun, core.PermGraphEdit,
	}}
	_, tok, err := auth.IssueAPIKey(h.ks, t.Context(), "k-mcp", "t", "ws", "alice", []core.Role{role}, nil)
	if err != nil {
		t.Fatalf("issue mcp key: %v", err)
	}
	covSeedFlow(t, h, "f1")
	rw := sessionDo(t, h, tok, "DELETE", "/api/v1/me/flows/"+cov3FlowID, nil)
	if rw.Code != http.StatusForbidden {
		t.Fatalf("mcp-scoped key delete = %d (%s), want 403", rw.Code, rw.Body.String())
	}
	if !strings.Contains(rw.Body.String(), "admin_scope_required") {
		t.Fatalf("want admin_scope_required code, got %s", rw.Body.String())
	}
	// The message has to name the way out, or the caller just retries.
	if !strings.Contains(rw.Body.String(), "graph:admin") {
		t.Fatalf("error should name the missing permission: %s", rw.Body.String())
	}
	if _, err := h.ws.Load("f1"); err != nil {
		t.Fatal("flow was deleted by a key without graph:admin")
	}
}

// A key can't smuggle itself past the scope check by supplying a password in
// the body — the credential kind picks the gate, not the body.
func TestDeleteFlowMe_APIKeyCannotUsePasswordGate(t *testing.T) {
	t.Parallel()
	h, _ := deleteFlowSessionHarness(t, "correct-pw")
	role := core.Role{Name: "claude-mcp", Permissions: []core.Permission{
		core.PermGraphRun, core.PermGraphEdit,
	}}
	_, tok, err := auth.IssueAPIKey(h.ks, t.Context(), "k-mcp2", "t", "ws", "alice", []core.Role{role}, nil)
	if err != nil {
		t.Fatalf("issue mcp key: %v", err)
	}
	covSeedFlow(t, h, "f1")
	rw := sessionDo(t, h, tok, "DELETE", "/api/v1/me/flows/"+cov3FlowID, map[string]any{"password": "correct-pw"})
	if rw.Code != http.StatusForbidden {
		t.Fatalf("key + password = %d (%s), want 403", rw.Code, rw.Body.String())
	}
	if _, err := h.ws.Load("f1"); err != nil {
		t.Fatal("flow was deleted by a key that supplied a password")
	}
}
