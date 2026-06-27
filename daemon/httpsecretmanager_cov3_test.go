// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"net/http"
	"testing"
)

// secretManagerGate forbidden branch: a configured store but a principal
// without the required secret permission.

func TestSecretManager_ForbiddenWithoutPerm(t *testing.T) {
	h := newGatewayHarness(t)
	h.gw.EncryptedSecrets = testEncryptedSecrets(t)
	// Default editor token lacks secret:read/write.
	cases := []struct {
		method, path string
	}{
		{"GET", "/api/v1/secret-manager"},
		{"PUT", "/api/v1/secret-manager"},
		{"DELETE", "/api/v1/secret-manager"},
		{"GET", "/api/v1/secret-manager/aws"},
		{"PUT", "/api/v1/secret-manager/aws"},
		{"DELETE", "/api/v1/secret-manager/aws"},
		{"GET", "/api/v1/secret-manager/gcp"},
		{"PUT", "/api/v1/secret-manager/gcp"},
		{"DELETE", "/api/v1/secret-manager/gcp"},
	}
	for _, c := range cases {
		rw := h.do(t, c.method, c.path, map[string]any{})
		if rw.Code != http.StatusForbidden {
			t.Errorf("%s %s = %d (%s), want 403", c.method, c.path, rw.Code, rw.Body.String())
		}
	}
}

func TestSecretManagerCloud_NotConfigured(t *testing.T) {
	h := newGatewayHarness(t) // no EncryptedSecrets
	for _, path := range []string{
		"/api/v1/secret-manager/aws",
		"/api/v1/secret-manager/gcp",
	} {
		rw := h.do(t, "GET", path, nil)
		if rw.Code != http.StatusNotImplemented {
			t.Errorf("GET %s no store = %d (%s), want 501", path, rw.Code, rw.Body.String())
		}
	}
}
