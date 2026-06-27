package daemon

import (
	"encoding/json"
	"net/http"
	"testing"
)

// secretScopeFromRequest + authorizeFlowSecretScope branches.

func TestSecrets_UnknownScope(t *testing.T) {
	h := newSecretsHarness(t)
	rw := h.do(t, "GET", "/api/v1/secrets?scope=galaxy", nil)
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("unknown scope = %d (%s), want 400", rw.Code, rw.Body.String())
	}
}

func TestSecrets_FlowScopeMissingFlow(t *testing.T) {
	h := newSecretsHarness(t)
	rw := h.do(t, "GET", "/api/v1/secrets?scope=flow", nil)
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("scope=flow w/o flow = %d (%s), want 400", rw.Code, rw.Body.String())
	}
}

func TestSecrets_FlowScopeNonexistentFlowForbidden(t *testing.T) {
	h := newSecretsHarness(t)
	// No such flow -> authorizeFlowSecretScope reports forbidden (probe-proof).
	rw := h.do(t, "PUT", "/api/v1/secrets/MY_KEY?scope=flow&flow=ghost",
		json.RawMessage(putBody("v")))
	if rw.Code != http.StatusForbidden {
		t.Fatalf("ghost flow secret = %d (%s), want 403", rw.Code, rw.Body.String())
	}
}

func TestResources_UnknownScope(t *testing.T) {
	h := newSecretsHarness(t)
	rw := h.do(t, "GET", "/api/v1/resources?scope=galaxy", nil)
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("unknown resource scope = %d (%s), want 400", rw.Code, rw.Body.String())
	}
}

func TestResources_DeleteNotConfigured(t *testing.T) {
	h := newGatewayHarness(t) // no EncryptedSecrets
	rw := h.do(t, "DELETE", "/api/v1/resources/leads", nil)
	if rw.Code != http.StatusNotImplemented {
		t.Fatalf("delete resource no store = %d (%s), want 501", rw.Code, rw.Body.String())
	}
}

func TestResources_DeleteBadName(t *testing.T) {
	h := newSecretsHarness(t)
	rw := h.do(t, "DELETE", "/api/v1/resources/a.b", nil)
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("dotted name delete = %d (%s), want 400", rw.Code, rw.Body.String())
	}
}
