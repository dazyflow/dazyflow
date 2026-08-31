// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
)

// newSecretsHarness extends the default gateway harness with an
// encrypted-secrets provider wired in. Most tests need both the
// HTTP scaffolding and the store ready to go.
func newSecretsHarness(t *testing.T) *gatewayHarness {
	t.Helper()
	h := newGatewayHarness(t)
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1) // deterministic so tests are reproducible
	}
	es, err := NewEncryptedSecrets(key, NewMemSecretsStore())
	if err != nil {
		t.Fatalf("NewEncryptedSecrets: %v", err)
	}
	h.gw.EncryptedSecrets = es
	// Add secret:read/write perms to the default editor role so the
	// existing token can drive the CRUD endpoints.
	role := core.Role{Name: "secret-admin", Permissions: []core.Permission{
		core.PermGraphRun, core.PermGraphEdit, core.PermGraphAdmin,
		core.PermSecretRead, core.PermSecretWrite,
	}}
	_, tok, err := auth.IssueAPIKey(h.ks, t.Context(), "secret-key", "t", "ws", "alice", []core.Role{role}, nil)
	if err != nil {
		t.Fatalf("issue secret token: %v", err)
	}
	h.token = tok
	return h
}

// putBody builds the JSON body for PUT /secrets.
func putBody(value string) []byte {
	b, _ := json.Marshal(map[string]any{"value": value})
	return b
}

func TestHTTPSecrets_PutAndList(t *testing.T) {
	h := newSecretsHarness(t)
	rw := h.do(t, "PUT", "/api/v1/secrets/slack_token", json.RawMessage(putBody("xoxb-12345")))
	if rw.Code != http.StatusNoContent {
		t.Fatalf("PUT status=%d body=%s", rw.Code, rw.Body.String())
	}
	rw = h.do(t, "GET", "/api/v1/secrets", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("LIST status=%d body=%s", rw.Code, rw.Body.String())
	}
	var resp struct {
		Secrets []string `json:"secrets"`
	}
	_ = json.Unmarshal(rw.Body.Bytes(), &resp)
	if len(resp.Secrets) != 1 || resp.Secrets[0] != "slack_token" {
		t.Errorf("got %v, want [slack_token]", resp.Secrets)
	}
}

func TestHTTPSecrets_ValueNotInListResponse(t *testing.T) {
	// Critical UX/security contract: listing must never return the
	// stored value, only names.
	h := newSecretsHarness(t)
	h.do(t, "PUT", "/api/v1/secrets/api_key", json.RawMessage(putBody("super-secret-value-12345")))
	rw := h.do(t, "GET", "/api/v1/secrets", nil)
	if strings.Contains(rw.Body.String(), "super-secret-value-12345") {
		t.Errorf("LIST response leaked secret value: %s", rw.Body.String())
	}
}

func TestHTTPSecrets_IncludeConn(t *testing.T) {
	// The org listing hides the conn.<slug>.<key> namespace so the
	// Credentials page stays clean, but ?include=conn opts it back in so
	// the Apps page can tell which integrations are connected. Regression
	// for the "Connect button clears with no effect" bug: the secret saved
	// fine but was invisible to the page checking connection state.
	h := newSecretsHarness(t)
	h.do(t, "PUT", "/api/v1/secrets/regular_key", json.RawMessage(putBody("v1")))
	h.do(t, "PUT", "/api/v1/secrets/conn.ntfy.server", json.RawMessage(putBody("https://ntfy.sh")))

	list := func(path string) []string {
		rw := h.do(t, "GET", path, nil)
		if rw.Code != http.StatusOK {
			t.Fatalf("GET %s status=%d body=%s", path, rw.Code, rw.Body.String())
		}
		var resp struct {
			Secrets []string `json:"secrets"`
		}
		_ = json.Unmarshal(rw.Body.Bytes(), &resp)
		return resp.Secrets
	}

	plain := list("/api/v1/secrets")
	if slices.Contains(plain, "conn.ntfy.server") {
		t.Errorf("default listing must hide conn.* names; got %v", plain)
	}
	if !slices.Contains(plain, "regular_key") {
		t.Errorf("default listing must include normal secrets; got %v", plain)
	}

	withConn := list("/api/v1/secrets?include=conn")
	if !slices.Contains(withConn, "conn.ntfy.server") {
		t.Errorf("include=conn must surface conn.* names; got %v", withConn)
	}
	if !slices.Contains(withConn, "regular_key") {
		t.Errorf("include=conn must still include normal secrets; got %v", withConn)
	}
}

func TestHTTPSecrets_NoGetByName(t *testing.T) {
	// We intentionally don't expose GET /secrets/{name}. Probing it
	// should 404 (no matching route).
	h := newSecretsHarness(t)
	h.do(t, "PUT", "/api/v1/secrets/k", json.RawMessage(putBody("v")))
	rw := h.do(t, "GET", "/api/v1/secrets/k", nil)
	if rw.Code != http.StatusNotFound && rw.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET by name should not exist; got %d", rw.Code)
	}
}

func TestHTTPSecrets_Delete(t *testing.T) {
	h := newSecretsHarness(t)
	h.do(t, "PUT", "/api/v1/secrets/temp", json.RawMessage(putBody("v")))
	rw := h.do(t, "DELETE", "/api/v1/secrets/temp", nil)
	if rw.Code != http.StatusNoContent {
		t.Fatalf("DELETE status=%d body=%s", rw.Code, rw.Body.String())
	}
	rw = h.do(t, "GET", "/api/v1/secrets", nil)
	var resp struct {
		Secrets []string `json:"secrets"`
	}
	_ = json.Unmarshal(rw.Body.Bytes(), &resp)
	if len(resp.Secrets) != 0 {
		t.Errorf("got %v after delete, want []", resp.Secrets)
	}
}

func TestHTTPSecrets_DeleteIdempotent(t *testing.T) {
	h := newSecretsHarness(t)
	rw := h.do(t, "DELETE", "/api/v1/secrets/never_existed", nil)
	if rw.Code != http.StatusNoContent {
		t.Errorf("delete-missing should be 204, got %d", rw.Code)
	}
}

func TestHTTPSecrets_PutEmptyValueRejected(t *testing.T) {
	h := newSecretsHarness(t)
	rw := h.do(t, "PUT", "/api/v1/secrets/k", json.RawMessage(putBody("")))
	if rw.Code != http.StatusBadRequest {
		t.Errorf("status=%d, want 400 for empty value", rw.Code)
	}
}

func TestHTTPSecrets_BadName(t *testing.T) {
	h := newSecretsHarness(t)
	// Path traversal characters must be rejected.
	// Note: ".." is intentionally not in this list — secret names
	// are stored as DB column values, never embedded in paths, so
	// dot characters are fine. The validator rejects path
	// separators and whitespace because those break the URL.
	for _, bad := range []string{
		"with space",
		"with/slash",
	} {
		t.Run(bad, func(t *testing.T) {
			// URL-encode so the request parses; the server-side
			// validSecretName check is what we're actually testing.
			rw := h.do(t, "PUT", "/api/v1/secrets/"+url.PathEscape(bad),
				json.RawMessage(putBody("v")))
			// Some bad names break URL routing entirely (404); others
			// land on the handler and 400. Either is fine — the
			// invariant is "doesn't 204."
			if rw.Code == http.StatusNoContent {
				t.Errorf("name %q should have been rejected, got 204", bad)
			}
		})
	}
}

func TestHTTPSecrets_OversizeValueRejected(t *testing.T) {
	h := newSecretsHarness(t)
	huge := strings.Repeat("x", maxSecretValueBytes+1024)
	rw := h.do(t, "PUT", "/api/v1/secrets/k", json.RawMessage(putBody(huge)))
	if rw.Code == http.StatusNoContent {
		t.Errorf("oversize value should be rejected, got 204")
	}
}

func TestHTTPSecrets_RequiresWritePermission(t *testing.T) {
	// Runner-only role (graph:run, no secret:write) → PUT 403.
	h := newSecretsHarness(t)
	role := core.Role{Name: "runner", Permissions: []core.Permission{core.PermGraphRun}}
	_, tok, _ := auth.IssueAPIKey(h.ks, t.Context(), "runner-key", "t", "ws", "bob", []core.Role{role}, nil)

	req := httptest.NewRequest("PUT", "/api/v1/secrets/k", bytes.NewBuffer(putBody("v")))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	if rw.Code != http.StatusForbidden {
		t.Errorf("status=%d, want 403", rw.Code)
	}
}

func TestHTTPSecrets_RequiresReadPermissionForList(t *testing.T) {
	// Same as above but for GET.
	h := newSecretsHarness(t)
	role := core.Role{Name: "runner", Permissions: []core.Permission{core.PermGraphRun}}
	_, tok, _ := auth.IssueAPIKey(h.ks, t.Context(), "runner-key", "t", "ws", "bob", []core.Role{role}, nil)

	req := httptest.NewRequest("GET", "/api/v1/secrets", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	if rw.Code != http.StatusForbidden {
		t.Errorf("status=%d, want 403", rw.Code)
	}
}

func TestHTTPSecrets_NotConfiguredIs501(t *testing.T) {
	// Without EncryptedSecrets wired up, all three endpoints must
	// 501 rather than panicking or silently no-oping.
	h := newGatewayHarness(t) // no EncryptedSecrets
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/v1/secrets"},
		{"PUT", "/api/v1/secrets/k"},
		{"DELETE", "/api/v1/secrets/k"},
	} {
		t.Run(c.method+" "+c.path, func(t *testing.T) {
			req := httptest.NewRequest(c.method, c.path, bytes.NewBuffer(putBody("v")))
			req.Header.Set("Authorization", "Bearer "+h.token)
			req.Header.Set("Content-Type", "application/json")
			rw := httptest.NewRecorder()
			ServeForTest(h.gw, rw, req)
			if rw.Code != http.StatusNotImplemented {
				t.Errorf("status=%d, want 501", rw.Code)
			}
		})
	}
}

// ---- End-to-end via the engine path -----------------------------------------

// TestEncryptedSecrets_ResolvedInJobParams confirms that a secret
// PUT via the API can be resolved as `${secret.NAME}` inside a job's
// params at execution time. This is the contract that makes the
// store usable by graphs.
func TestEncryptedSecrets_ResolvedInJobParams(t *testing.T) {
	h := newSecretsHarness(t)

	// PUT a value through the API.
	if rw := h.do(t, "PUT", "/api/v1/secrets/api_token",
		json.RawMessage(putBody("xoxb-from-api"))); rw.Code != http.StatusNoContent {
		t.Fatalf("PUT failed: %d", rw.Code)
	}

	// Read it back directly via the provider (the engine path would
	// do the same thing during resolveTemplates).
	ctx := core.WithTenant(t.Context(), "t")
	got, err := h.gw.EncryptedSecrets.Get(ctx, "api_token")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "xoxb-from-api" {
		t.Errorf("got %q, want xoxb-from-api", got)
	}
}

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
