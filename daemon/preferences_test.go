// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
)

// newPrefsHarness wires the password stores + auth chain and seeds one
// password user, the same way newTOTPHarness does, minus the TOTP key —
// the preferences surface only needs sign-in to work.
func newPrefsHarness(t *testing.T) (*gatewayHarness, auth.User, string) {
	t.Helper()
	h := newGatewayHarness(t)
	h.gw.Users, _ = auth.OpenJSONUserStore("")
	h.gw.Sessions = auth.NewMemSessionStore()
	h.svc.Auth = auth.Chain{
		&auth.APIKeyAuthenticator{Store: h.ks},
		&auth.SessionAuthenticator{Store: h.gw.Sessions},
	}
	pw := "correct horse battery staple"
	hash, _ := auth.HashPassword(pw)
	u := auth.User{
		Email:        "owner@example.com",
		PasswordHash: hash,
		Subject:      "owner@example.com",
		Tenant:       "t",
		Workspace:    "ws",
		Roles:        []core.Role{{Name: "editor", Permissions: []core.Permission{core.PermGraphRun}}},
	}
	if err := h.gw.Users.PutUser(t.Context(), u); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return h, u, pw
}

func getPrefs(t *testing.T, h *gatewayHarness, token string) preferencesResponse {
	t.Helper()
	rw := bearerDo(t, h, token, "GET", "/api/v1/me/preferences", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("GET prefs status=%d body=%s", rw.Code, rw.Body.String())
	}
	var p preferencesResponse
	if err := json.Unmarshal(rw.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode prefs: %v", err)
	}
	return p
}

// A fresh account has never touched its preferences, so the
// flow-failure email defaults to ON (the opt-out model).
func TestPreferences_DefaultsOn(t *testing.T) {
	t.Parallel()
	h, u, pw := newPrefsHarness(t)
	token := sessionTokenFor(t, h, u.Email, pw)
	if got := getPrefs(t, h, token); !got.EmailOnFlowFailure {
		t.Fatalf("fresh account: email_on_flow_failure=%v, want true (default on)", got.EmailOnFlowFailure)
	}
}

// PUT persists an explicit choice; a subsequent GET reflects it, and the
// stored user carries the non-nil (explicit) pointer.
func TestPreferences_PutRoundTrips(t *testing.T) {
	t.Parallel()
	h, u, pw := newPrefsHarness(t)
	token := sessionTokenFor(t, h, u.Email, pw)

	rw := bearerDo(t, h, token, "PUT", "/api/v1/me/preferences",
		map[string]any{"email_on_flow_failure": false})
	if rw.Code != http.StatusOK {
		t.Fatalf("PUT prefs status=%d body=%s", rw.Code, rw.Body.String())
	}
	if got := getPrefs(t, h, token); got.EmailOnFlowFailure {
		t.Fatalf("after PUT off: email_on_flow_failure=%v, want false", got.EmailOnFlowFailure)
	}

	// The stored user should now carry an explicit (non-nil) preference,
	// not the implicit default.
	stored, err := h.gw.Users.GetByEmail(t.Context(), u.Email)
	if err != nil {
		t.Fatalf("reload user: %v", err)
	}
	if stored.Notify.EmailOnFlowFailure == nil {
		t.Fatalf("expected an explicit (non-nil) EmailOnFlowFailure after PUT")
	}
	if *stored.Notify.EmailOnFlowFailure {
		t.Fatalf("stored pref = true, want false")
	}

	// Toggle back on.
	rw = bearerDo(t, h, token, "PUT", "/api/v1/me/preferences",
		map[string]any{"email_on_flow_failure": true})
	if rw.Code != http.StatusOK {
		t.Fatalf("PUT prefs (on) status=%d body=%s", rw.Code, rw.Body.String())
	}
	if got := getPrefs(t, h, token); !got.EmailOnFlowFailure {
		t.Fatalf("after PUT on: email_on_flow_failure=%v, want true", got.EmailOnFlowFailure)
	}
}

// Theme + language round-trip, and PUT is partial: setting the theme
// alone must not disturb the (default-on) notification preference.
func TestPreferences_ThemeAndLanguageRoundTrip(t *testing.T) {
	t.Parallel()
	h, u, pw := newPrefsHarness(t)
	token := sessionTokenFor(t, h, u.Email, pw)

	rw := bearerDo(t, h, token, "PUT", "/api/v1/me/preferences",
		map[string]any{"theme": "light"})
	if rw.Code != http.StatusOK {
		t.Fatalf("PUT theme status=%d body=%s", rw.Code, rw.Body.String())
	}
	got := getPrefs(t, h, token)
	if got.Theme != "light" {
		t.Errorf("theme=%q, want light", got.Theme)
	}
	// Partial update: theme PUT didn't carry notify, so it must stay on.
	if !got.EmailOnFlowFailure {
		t.Errorf("theme PUT clobbered email_on_flow_failure to %v, want true", got.EmailOnFlowFailure)
	}

	rw = bearerDo(t, h, token, "PUT", "/api/v1/me/preferences",
		map[string]any{"language": "sv"})
	if rw.Code != http.StatusOK {
		t.Fatalf("PUT language status=%d body=%s", rw.Code, rw.Body.String())
	}
	got = getPrefs(t, h, token)
	if got.Language != "sv" {
		t.Errorf("language=%q, want sv", got.Language)
	}
	// And the language PUT preserved the earlier theme choice.
	if got.Theme != "light" {
		t.Errorf("language PUT clobbered theme to %q, want light", got.Theme)
	}

	// "system" (follow the OS) is the web's default choice and must round-trip
	// like any other value — if it were rejected, or silently stored as "",
	// picking System would never roam to the user's other devices.
	rw = bearerDo(t, h, token, "PUT", "/api/v1/me/preferences",
		map[string]any{"theme": "system"})
	if rw.Code != http.StatusOK {
		t.Fatalf("PUT theme=system status=%d body=%s", rw.Code, rw.Body.String())
	}
	if got = getPrefs(t, h, token); got.Theme != "system" {
		t.Errorf("theme=%q, want system", got.Theme)
	}
}

// Invalid theme / language values are rejected before anything is
// written.
func TestPreferences_RejectsInvalidValues(t *testing.T) {
	t.Parallel()
	h, u, pw := newPrefsHarness(t)
	token := sessionTokenFor(t, h, u.Email, pw)

	rw := bearerDo(t, h, token, "PUT", "/api/v1/me/preferences",
		map[string]any{"theme": "neon"})
	if rw.Code != http.StatusBadRequest {
		t.Errorf("PUT bad theme status=%d, want 400; body=%s", rw.Code, rw.Body.String())
	}
	rw = bearerDo(t, h, token, "PUT", "/api/v1/me/preferences",
		map[string]any{"language": "not a locale"})
	if rw.Code != http.StatusBadRequest {
		t.Errorf("PUT bad language status=%d, want 400; body=%s", rw.Code, rw.Body.String())
	}
	// Nothing should have been persisted by the rejected writes.
	if got := getPrefs(t, h, token); got.Theme != "" || got.Language != "" {
		t.Errorf("rejected writes leaked: theme=%q language=%q", got.Theme, got.Language)
	}
}

// An API-key principal has no password-user record. GET returns the
// defaults rather than erroring (mirrors totpStatus); PUT 400s because
// there's no account to write to.
func TestPreferences_APIKeyPrincipalGetsDefaults(t *testing.T) {
	t.Parallel()
	h, _, _ := newPrefsHarness(t)
	// h.do() authenticates with the harness's editor API key — a
	// principal with no user-store record.
	rw := h.do(t, "GET", "/api/v1/me/preferences", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("GET prefs (api key) status=%d body=%s", rw.Code, rw.Body.String())
	}
	var p preferencesResponse
	if err := json.Unmarshal(rw.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !p.EmailOnFlowFailure {
		t.Fatalf("api-key principal defaults: email_on_flow_failure=%v, want true", p.EmailOnFlowFailure)
	}

	rw = h.do(t, "PUT", "/api/v1/me/preferences",
		map[string]any{"email_on_flow_failure": false})
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("PUT prefs (api key) status=%d, want 400; body=%s", rw.Code, rw.Body.String())
	}
}

func TestGetPreferences_NotConfigured(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t) // Users nil
	rw := h.do(t, "GET", "/api/v1/me/preferences", nil)
	if rw.Code != http.StatusNotImplemented {
		t.Fatalf("get prefs no Users = %d (%s), want 501", rw.Code, rw.Body.String())
	}
}

func TestGetPreferences_UnknownUserDefaults(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	h := newGatewayHarness(t)
	rw := h.do(t, "PUT", "/api/v1/me/preferences", map[string]any{"theme": "dark"})
	if rw.Code != http.StatusNotImplemented {
		t.Fatalf("put prefs no Users = %d (%s), want 501", rw.Code, rw.Body.String())
	}
}

func TestPutPreferences_NoUser(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)
	users, _ := auth.OpenJSONUserStore("")
	h.gw.Users = users
	// Valid body but no password-user record for the API-key principal.
	rw := h.do(t, "PUT", "/api/v1/me/preferences", map[string]any{"theme": "dark"})
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("put prefs no user = %d (%s), want 400", rw.Code, rw.Body.String())
	}
}
