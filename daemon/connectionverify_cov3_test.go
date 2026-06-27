// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"net/http"
	"testing"
)

// Early-guard branches of the integration connection PUT / verify handlers.

func TestPutConnection_NotConfigured(t *testing.T) {
	h := newGatewayHarness(t) // no EncryptedSecrets
	rw := h.do(t, "PUT", "/api/v1/catalog/integrations/slack/connection", map[string]any{})
	if rw.Code != http.StatusNotImplemented {
		t.Fatalf("put conn no store = %d (%s), want 501", rw.Code, rw.Body.String())
	}
}

func TestPutConnection_PermissionDenied(t *testing.T) {
	h := newGatewayHarness(t)
	h.gw.EncryptedSecrets = testEncryptedSecrets(t)
	// Default editor token lacks secret:write.
	rw := h.do(t, "PUT", "/api/v1/catalog/integrations/slack/connection", map[string]any{})
	if rw.Code != http.StatusForbidden {
		t.Fatalf("put conn no perm = %d (%s), want 403", rw.Code, rw.Body.String())
	}
}

func TestPutConnection_IntegrationNotFound(t *testing.T) {
	h := newSecretsHarness(t)
	h.gw.EncryptedSecrets = testEncryptedSecrets(t)
	rw := h.do(t, "PUT", "/api/v1/catalog/integrations/no_such_integration_xyz/connection",
		map[string]any{"values": map[string]string{}})
	if rw.Code != http.StatusNotFound {
		t.Fatalf("put conn unknown integration = %d (%s), want 404", rw.Code, rw.Body.String())
	}
}

func TestVerifyConnection_NotConfigured(t *testing.T) {
	h := newGatewayHarness(t)
	rw := h.do(t, "POST", "/api/v1/catalog/integrations/slack/verify", nil)
	if rw.Code != http.StatusNotImplemented {
		t.Fatalf("verify no store = %d (%s), want 501", rw.Code, rw.Body.String())
	}
}

func TestVerifyConnection_PermissionDenied(t *testing.T) {
	h := newGatewayHarness(t)
	h.gw.EncryptedSecrets = testEncryptedSecrets(t)
	rw := h.do(t, "POST", "/api/v1/catalog/integrations/slack/verify", nil)
	if rw.Code != http.StatusForbidden {
		t.Fatalf("verify no perm = %d (%s), want 403", rw.Code, rw.Body.String())
	}
}

func TestVerifyConnection_NotVerifiable(t *testing.T) {
	h := newSecretsHarness(t)
	h.gw.EncryptedSecrets = testEncryptedSecrets(t)
	// An integration slug with no registered verifier -> 501 not_verifiable.
	rw := h.do(t, "POST", "/api/v1/catalog/integrations/no_such_integration_xyz/verify", nil)
	if rw.Code != http.StatusNotImplemented {
		t.Fatalf("verify unverifiable = %d (%s), want 501", rw.Code, rw.Body.String())
	}
}
