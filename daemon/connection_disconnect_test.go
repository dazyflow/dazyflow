package daemon

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"git.sr.ht/~klahr/hazyflow/auth"
	"git.sr.ht/~klahr/hazyflow/core"
)

// Disconnecting a connection deletes the stored oauth.<provider>.<account>
// token for the caller's tenant. It needs secret:write (same as connect),
// rejects unknown providers, and is idempotent.
func TestDisconnectConnection(t *testing.T) {
	h := newAdminOAuthHarness(t) // wires EncryptedSecrets + OAuth registry
	es := h.gw.EncryptedSecrets
	name := secretNameFor("google", "default")
	if err := es.Put(t.Context(), "t", name, `{"access_token":"x"}`); err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	// A token that has secret:write (the editor harness token does not).
	swRole := core.Role{Name: "editor", Permissions: []core.Permission{core.PermSecretWrite}}
	_, swTok, err := auth.IssueAPIKey(h.ks, t.Context(), "k-sw", "t", "ws", "u@t", []core.Role{swRole}, nil)
	if err != nil {
		t.Fatalf("issue secret:write key: %v", err)
	}
	do := func(tok, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("DELETE", path, nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		rw := httptest.NewRecorder()
		ServeForTest(h.gw, rw, req)
		return rw
	}

	// Without secret:write → 403, and the connection is untouched.
	if rw := h.do(t, "DELETE", "/api/v1/me/connections/google", nil); rw.Code != http.StatusForbidden {
		t.Errorf("no secret:write should be 403; got %d", rw.Code)
	}
	// Unknown provider → 404.
	if rw := do(swTok, "/api/v1/me/connections/discord"); rw.Code != http.StatusNotFound {
		t.Errorf("unknown provider should be 404; got %d body=%s", rw.Code, rw.Body.String())
	}
	// Still present before the authorized delete.
	if got, err := es.Get(core.WithTenant(t.Context(), "t"), name); err != nil || got == "" {
		t.Fatalf("precondition: connection should still exist (got=%q err=%v)", got, err)
	}

	// secret:write → 204, and the token is gone.
	if rw := do(swTok, "/api/v1/me/connections/google"); rw.Code != http.StatusNoContent {
		t.Fatalf("disconnect should be 204; got %d body=%s", rw.Code, rw.Body.String())
	}
	if got, err := es.Get(core.WithTenant(t.Context(), "t"), name); err == nil && got != "" {
		t.Errorf("connection should be gone after disconnect; got %q", got)
	}

	// Idempotent: deleting an already-gone connection still succeeds.
	if rw := do(swTok, "/api/v1/me/connections/google"); rw.Code != http.StatusNoContent {
		t.Errorf("second disconnect should be idempotent 204; got %d", rw.Code)
	}
}
