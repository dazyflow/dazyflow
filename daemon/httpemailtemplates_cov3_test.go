package daemon

import (
	"net/http"
	"testing"
)

// emailTemplateGate not-configured branch (no EncryptedSecrets) across the
// list / put / delete / preview / send-test endpoints.

func TestEmailTemplates_NotConfigured(t *testing.T) {
	h := newGatewayHarness(t) // no EncryptedSecrets
	cases := []struct {
		method, path string
	}{
		{"GET", "/api/v1/email-templates"},
		{"PUT", "/api/v1/email-templates/welcome"},
		{"DELETE", "/api/v1/email-templates/welcome"},
		{"POST", "/api/v1/email-templates/preview"},
		{"POST", "/api/v1/email-templates/send-test"},
	}
	for _, c := range cases {
		rw := h.adminDo(t, c.method, c.path, map[string]any{})
		if rw.Code != http.StatusNotImplemented {
			t.Errorf("%s %s = %d (%s), want 501", c.method, c.path, rw.Code, rw.Body.String())
		}
	}
}

func TestEmailTemplates_ListForbiddenWithoutReadPerm(t *testing.T) {
	h := newGatewayHarness(t)
	h.gw.EncryptedSecrets = testEncryptedSecrets(t)
	// Default editor token lacks secret:read.
	rw := h.do(t, "GET", "/api/v1/email-templates", nil)
	if rw.Code != http.StatusForbidden {
		t.Fatalf("list templates no perm = %d (%s), want 403", rw.Code, rw.Body.String())
	}
}
