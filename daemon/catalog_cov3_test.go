package daemon

import (
	"net/http"
	"testing"

	"git.sr.ht/~klahr/dazyflow/auth"
	"git.sr.ht/~klahr/dazyflow/core"
)

func TestListMyAPIKeys_NotConfigured(t *testing.T) {
	h := newGatewayHarness(t)
	h.svc.AdminKeys = nil
	rw := h.do(t, "GET", "/api/v1/me/api-keys", nil)
	if rw.Code != http.StatusNotImplemented {
		t.Fatalf("list keys no admin = %d (%s), want 501", rw.Code, rw.Body.String())
	}
}

func TestRevokeMyAPIKey_NotConfigured(t *testing.T) {
	h := newGatewayHarness(t)
	h.svc.AdminKeys = nil
	rw := h.do(t, "DELETE", "/api/v1/me/api-keys/abc", nil)
	if rw.Code != http.StatusNotImplemented {
		t.Fatalf("revoke key no admin = %d (%s), want 501", rw.Code, rw.Body.String())
	}
}

func TestRevokeMyAPIKey_OtherUsersKeyIs404(t *testing.T) {
	h := newGatewayHarness(t)
	// Issue a key owned by a different subject ("bob"); the harness caller
	// "alice" must get 404 (not 403) to avoid leaking key existence.
	role := core.Role{Name: "viewer", Permissions: []core.Permission{core.PermGraphRun}}
	key, _, err := auth.IssueAPIKey(h.ks, t.Context(), "bobs-key", "t", "ws", "bob", []core.Role{role}, nil)
	if err != nil {
		t.Fatalf("issue bob key: %v", err)
	}
	rw := h.do(t, "DELETE", "/api/v1/me/api-keys/"+key.ID, nil)
	if rw.Code != http.StatusNotFound {
		t.Fatalf("revoke other's key = %d (%s), want 404", rw.Code, rw.Body.String())
	}
}
