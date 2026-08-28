// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/auth"
	"git.sr.ht/~klahr/dazyflow/core"
)

// newSecretManagerHarness is newSecretsHarness with the token swapped for an
// organization:admin one: configuring a BYO secret-manager backend (Vault/AWS/
// GCP) is an infrastructure action gated on organization:admin, not secret:write
// — the PUT connection-tests a tenant-supplied address, so an editor must not be
// able to point the org at, or probe via, an arbitrary host. (The fake provider
// servers run on loopback; the package TestMain allows private egress so the
// secret-manager clients' SSRF guard doesn't refuse them.)
func newSecretManagerHarness(t *testing.T) *gatewayHarness {
	t.Helper()
	h := newSecretsHarness(t)
	_, tok, err := auth.IssueAPIKey(h.ks, t.Context(), "sm-admin", "t", "ws", "admin@t", []core.Role{core.TeamRoleAdmin()}, nil)
	if err != nil {
		t.Fatalf("issue admin token: %v", err)
	}
	h.token = tok
	return h
}

// fakeVaultServer stands in for OpenBao/Vault: it accepts a token-self lookup
// from one known token and 403s anything else, so the verify-on-save path is
// exercised without a real server.
func fakeVaultServer(t *testing.T, goodToken string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/v1/auth/token/lookup-self") {
			if r.Header.Get("X-Vault-Token") == goodToken {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"data":{}}`))
				return
			}
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"errors":["permission denied"]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func smBody(addr, token string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"address": addr,
		"mount":   "secret",
		"auth":    map[string]any{"method": "token", "token": token},
	})
	return b
}

func TestSecretManager_SetGetDelete(t *testing.T) {
	h := newSecretManagerHarness(t)
	srv := fakeVaultServer(t, "good-token")

	// Save a valid config → verified, then stored.
	if rw := h.do(t, "PUT", "/api/v1/secret-manager", smBody(srv.URL, "good-token")); rw.Code != http.StatusNoContent {
		t.Fatalf("PUT status=%d body=%s", rw.Code, rw.Body.String())
	}

	// GET returns the redacted view — configured, address shown, credential NOT.
	rw := h.do(t, "GET", "/api/v1/secret-manager", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("GET status=%d body=%s", rw.Code, rw.Body.String())
	}
	if strings.Contains(rw.Body.String(), "good-token") {
		t.Fatalf("GET leaked the token: %s", rw.Body.String())
	}
	var view secretManagerView
	if err := json.Unmarshal(rw.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if !view.Configured || view.Address != srv.URL || view.AuthMethod != "token" {
		t.Errorf("view = %+v", view)
	}

	// The config must not appear in the user-facing secret listing.
	rw = h.do(t, "GET", "/api/v1/secrets", nil)
	if strings.Contains(rw.Body.String(), "cfg:secret-manager") {
		t.Errorf("secret-manager config leaked into the secret listing: %s", rw.Body.String())
	}

	// Delete → gone.
	if rw := h.do(t, "DELETE", "/api/v1/secret-manager", nil); rw.Code != http.StatusNoContent {
		t.Fatalf("DELETE status=%d", rw.Code)
	}
	rw = h.do(t, "GET", "/api/v1/secret-manager", nil)
	if err := json.Unmarshal(rw.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Configured {
		t.Error("config should be gone after delete")
	}
}

// A config that fails the connection test is rejected (502) and not persisted.
func TestSecretManager_RejectsUnreachable(t *testing.T) {
	h := newSecretManagerHarness(t)
	srv := fakeVaultServer(t, "good-token")

	if rw := h.do(t, "PUT", "/api/v1/secret-manager", smBody(srv.URL, "WRONG-token")); rw.Code != http.StatusBadGateway {
		t.Fatalf("PUT with bad token status=%d body=%s, want 502", rw.Code, rw.Body.String())
	}
	// Nothing stored.
	rw := h.do(t, "GET", "/api/v1/secret-manager", nil)
	var view secretManagerView
	_ = json.Unmarshal(rw.Body.Bytes(), &view)
	if view.Configured {
		t.Error("a rejected config must not be persisted")
	}
}

// An invalid config (bad auth method) is a 400 before any network call.
func TestSecretManager_ValidatesBody(t *testing.T) {
	h := newSecretManagerHarness(t)
	bad, _ := json.Marshal(map[string]any{"address": "https://v", "mount": "secret", "auth": map[string]any{"method": "psychic"}})
	if rw := h.do(t, "PUT", "/api/v1/secret-manager", json.RawMessage(bad)); rw.Code != http.StatusBadRequest {
		t.Fatalf("PUT invalid status=%d, want 400", rw.Code)
	}
}

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

// TestSecretManagerConfig_RequiresOrgAdmin pins the privilege boundary: an
// editor with secret:read/write can read which backend is configured (GET) but
// cannot point the org at a new secret-manager backend or remove it (PUT/DELETE)
// — those are organization:admin. The PUT is rejected at the gate, before the
// tenant-supplied address is ever dialed.
func TestSecretManagerConfig_RequiresOrgAdmin(t *testing.T) {
	h := newSecretsHarness(t) // editor token: secret:read/write, NOT organization:admin
	write := []struct{ method, path string }{
		{"PUT", "/api/v1/secret-manager"},
		{"DELETE", "/api/v1/secret-manager"},
		{"PUT", "/api/v1/secret-manager/aws"},
		{"DELETE", "/api/v1/secret-manager/aws"},
		{"PUT", "/api/v1/secret-manager/gcp"},
		{"DELETE", "/api/v1/secret-manager/gcp"},
	}
	for _, c := range write {
		if rw := h.do(t, c.method, c.path, map[string]any{}); rw.Code != http.StatusForbidden {
			t.Errorf("%s %s as editor = %d (%s), want 403", c.method, c.path, rw.Code, rw.Body.String())
		}
	}
	// GET stays at secret:read, so the editor can still see the backend status.
	for _, path := range []string{"/api/v1/secret-manager", "/api/v1/secret-manager/aws", "/api/v1/secret-manager/gcp"} {
		if rw := h.do(t, "GET", path, nil); rw.Code != http.StatusOK {
			t.Errorf("GET %s as editor = %d (%s), want 200", path, rw.Code, rw.Body.String())
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
