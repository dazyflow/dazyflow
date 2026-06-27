package daemon

import (
	"net/http"
	"testing"
)

// HTTP-handler branches of the /git/credentials endpoints: not-configured,
// permission, decode, and validation errors.

func TestGitCreds_NotConfigured(t *testing.T) {
	h := newGatewayHarness(t) // no EncryptedSecrets
	if rw := h.do(t, "GET", "/api/v1/git/credentials", nil); rw.Code != http.StatusNotImplemented {
		t.Fatalf("list w/o store = %d, want 501", rw.Code)
	}
	if rw := h.do(t, "PUT", "/api/v1/git/credentials/acct", map[string]any{"token": "x"}); rw.Code != http.StatusNotImplemented {
		t.Fatalf("put w/o store = %d, want 501", rw.Code)
	}
	if rw := h.do(t, "DELETE", "/api/v1/git/credentials/acct", nil); rw.Code != http.StatusNotImplemented {
		t.Fatalf("delete w/o store = %d, want 501", rw.Code)
	}
}

func TestGitCreds_ForbiddenWithoutSecretPerm(t *testing.T) {
	h := newGatewayHarness(t)
	h.gw.EncryptedSecrets = testEncryptedSecrets(t)
	// Default editor token lacks secret:read/write.
	if rw := h.do(t, "GET", "/api/v1/git/credentials", nil); rw.Code != http.StatusForbidden {
		t.Fatalf("list w/o perm = %d (%s), want 403", rw.Code, rw.Body.String())
	}
	if rw := h.do(t, "PUT", "/api/v1/git/credentials/acct", map[string]any{"token": "x"}); rw.Code != http.StatusForbidden {
		t.Fatalf("put w/o perm = %d, want 403", rw.Code)
	}
	if rw := h.do(t, "DELETE", "/api/v1/git/credentials/acct", nil); rw.Code != http.StatusForbidden {
		t.Fatalf("delete w/o perm = %d, want 403", rw.Code)
	}
}

func TestGitCreds_PutDecodeError(t *testing.T) {
	h := newSecretsHarness(t)
	h.gw.EncryptedSecrets = testEncryptedSecrets(t)
	req := newRawReq(t, h, "PUT", "/api/v1/git/credentials/acct", "{not json")
	rw := serveRaw(h, req)
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("put malformed = %d (%s), want 400", rw.Code, rw.Body.String())
	}
}

func TestGitCreds_PutInvalidCredential(t *testing.T) {
	h := newSecretsHarness(t)
	h.gw.EncryptedSecrets = testEncryptedSecrets(t)
	// Empty bundle (no key, no token) is invalid.
	rw := h.do(t, "PUT", "/api/v1/git/credentials/acct", map[string]any{})
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("put empty bundle = %d (%s), want 400", rw.Code, rw.Body.String())
	}
}

func TestGitCreds_PutThenListThenDelete(t *testing.T) {
	h := newSecretsHarness(t)
	es := testEncryptedSecrets(t)
	h.gw.EncryptedSecrets = es
	if rw := h.do(t, "PUT", "/api/v1/git/credentials/github", map[string]any{
		"token": "ghp_token", "username": "git",
	}); rw.Code != http.StatusNoContent {
		t.Fatalf("put = %d (%s), want 204", rw.Code, rw.Body.String())
	}
	rw := h.do(t, "GET", "/api/v1/git/credentials", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("list = %d (%s), want 200", rw.Code, rw.Body.String())
	}
	if rw := h.do(t, "DELETE", "/api/v1/git/credentials/github", nil); rw.Code != http.StatusNoContent {
		t.Fatalf("delete = %d (%s), want 204", rw.Code, rw.Body.String())
	}
}
