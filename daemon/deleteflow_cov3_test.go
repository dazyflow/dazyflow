package daemon

import (
	"net/http"
	"testing"

	"git.sr.ht/~klahr/dazyflow/auth"
)

// deleteFlowMe is password-gated: it needs h.Users configured and the caller's
// account password in the body. These cover the not-configured / bad-password /
// success branches the service-level tests don't reach.

func TestDeleteFlowMe_NotConfigured(t *testing.T) {
	h := newGatewayHarness(t) // Users is nil
	covSeedFlow(t, h, "f1")
	rw := h.do(t, "DELETE", "/api/v1/me/flows/"+cov3FlowID, map[string]any{"password": "x"})
	if rw.Code != http.StatusNotImplemented {
		t.Fatalf("delete flow no Users = %d (%s), want 501", rw.Code, rw.Body.String())
	}
}

func TestDeleteFlowMe_BadPassword(t *testing.T) {
	h := newGatewayHarness(t)
	users, _ := auth.OpenJSONUserStore("")
	hash, _ := auth.HashPassword("correct-pw")
	// The editor token's subject is "alice".
	_ = users.PutUser(t.Context(), auth.User{
		Email: "alice", Subject: "alice", PasswordHash: hash, Tenant: "t", Workspace: "ws",
	})
	h.gw.Users = users
	covSeedFlow(t, h, "f1")
	rw := h.do(t, "DELETE", "/api/v1/me/flows/"+cov3FlowID, map[string]any{"password": "wrong"})
	if rw.Code != http.StatusUnauthorized {
		t.Fatalf("delete flow bad pw = %d (%s), want 401", rw.Code, rw.Body.String())
	}
}

func TestDeleteFlowMe_OK(t *testing.T) {
	h := newGatewayHarness(t)
	users, _ := auth.OpenJSONUserStore("")
	hash, _ := auth.HashPassword("correct-pw")
	_ = users.PutUser(t.Context(), auth.User{
		Email: "alice", Subject: "alice", PasswordHash: hash, Tenant: "t", Workspace: "ws",
	})
	h.gw.Users = users
	covSeedFlow(t, h, "f1")
	rw := h.do(t, "DELETE", "/api/v1/me/flows/"+cov3FlowID, map[string]any{"password": "correct-pw"})
	if rw.Code != http.StatusNoContent {
		t.Fatalf("delete flow = %d (%s), want 204", rw.Code, rw.Body.String())
	}
	// Idempotent: deleting again is a no-op success (the flow is gone).
	rw = h.do(t, "DELETE", "/api/v1/me/flows/"+cov3FlowID, map[string]any{"password": "correct-pw"})
	if rw.Code != http.StatusNoContent {
		t.Fatalf("re-delete flow = %d (%s), want 204", rw.Code, rw.Body.String())
	}
}
