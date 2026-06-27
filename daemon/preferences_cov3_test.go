// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"net/http"
	"testing"

	"git.sr.ht/~klahr/dazyflow/auth"
)

func TestGetPreferences_NotConfigured(t *testing.T) {
	h := newGatewayHarness(t) // Users nil
	rw := h.do(t, "GET", "/api/v1/me/preferences", nil)
	if rw.Code != http.StatusNotImplemented {
		t.Fatalf("get prefs no Users = %d (%s), want 501", rw.Code, rw.Body.String())
	}
}

func TestGetPreferences_UnknownUserDefaults(t *testing.T) {
	h := newGatewayHarness(t)
	users, _ := auth.OpenJSONUserStore("")
	h.gw.Users = users
	// API-key principal "alice" has no password-user record -> defaults, 200.
	rw := h.do(t, "GET", "/api/v1/me/preferences", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("get prefs unknown user = %d (%s), want 200", rw.Code, rw.Body.String())
	}
}

func TestPutPreferences_NotConfigured(t *testing.T) {
	h := newGatewayHarness(t)
	rw := h.do(t, "PUT", "/api/v1/me/preferences", map[string]any{"theme": "dark"})
	if rw.Code != http.StatusNotImplemented {
		t.Fatalf("put prefs no Users = %d (%s), want 501", rw.Code, rw.Body.String())
	}
}

func TestPutPreferences_NoUser(t *testing.T) {
	h := newGatewayHarness(t)
	users, _ := auth.OpenJSONUserStore("")
	h.gw.Users = users
	// Valid body but no password-user record for the API-key principal.
	rw := h.do(t, "PUT", "/api/v1/me/preferences", map[string]any{"theme": "dark"})
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("put prefs no user = %d (%s), want 400", rw.Code, rw.Body.String())
	}
}
