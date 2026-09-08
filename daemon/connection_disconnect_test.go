// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
)

func TestDisconnectConnection(t *testing.T) {
	t.Parallel()
	h := newAdminOAuthHarness(t) // wires EncryptedSecrets + OAuth registry
	es := h.gw.EncryptedSecrets
	name := secretNameFor("google", "default")
	if err := es.Put(t.Context(), "t", name, `{"access_token":"x"}`); err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	swRole := core.Role{Name: "editor", Permissions: []core.Permission{core.PermSecretWrite}}
	_, swTok, err := auth.IssueAPIKey(h.ks, t.Context(), "k-sw", "t", "ws", "u@t", []core.Role{swRole}, nil)
	if err != nil {
		t.Fatalf("issue secret:write key: %v", err)
	}
	adminRole := core.Role{Name: "org-admin", Permissions: []core.Permission{core.PermOrganizationAdmin}}
	_, adminTok, err := auth.IssueAPIKey(h.ks, t.Context(), "k-admin", "t", "ws", "a@t", []core.Role{adminRole}, nil)
	if err != nil {
		t.Fatalf("issue org-admin key: %v", err)
	}
	do := func(tok, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("DELETE", path, nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		rw := httptest.NewRecorder()
		ServeForTest(h.gw, rw, req)
		return rw
	}

	if rw := h.do(t, "DELETE", "/api/v1/me/connections/google", nil); rw.Code != http.StatusForbidden {
		t.Errorf("no secret:write should be 403; got %d", rw.Code)
	}
	// secret:write but NOT org-admin → 403 for Google (the org-admin gate).
	if rw := do(swTok, "/api/v1/me/connections/google"); rw.Code != http.StatusForbidden {
		t.Errorf("secret:write without org-admin should be 403 for google; got %d body=%s", rw.Code, rw.Body.String())
	}
	if rw := do(swTok, "/api/v1/me/connections/discord"); rw.Code != http.StatusNotFound {
		t.Errorf("unknown provider should be 404; got %d body=%s", rw.Code, rw.Body.String())
	}
	if got, err := es.Get(core.WithTenant(t.Context(), "t"), name); err != nil || got == "" {
		t.Fatalf("precondition: connection should still exist (got=%q err=%v)", got, err)
	}

	if rw := do(adminTok, "/api/v1/me/connections/google"); rw.Code != http.StatusNoContent {
		t.Fatalf("disconnect should be 204; got %d body=%s", rw.Code, rw.Body.String())
	}
	if got, err := es.Get(core.WithTenant(t.Context(), "t"), name); err == nil && got != "" {
		t.Errorf("connection should be gone after disconnect; got %q", got)
	}

	if rw := do(adminTok, "/api/v1/me/connections/google"); rw.Code != http.StatusNoContent {
		t.Errorf("second disconnect should be idempotent 204; got %d", rw.Code)
	}
}
