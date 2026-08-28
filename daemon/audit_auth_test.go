// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

// auditActions returns the set of recorded actions for a tenant, for
// order-independent assertions.
func auditActions(t *testing.T, log *MemAuditLog, tenant string) map[string]core.AuditEvent {
	t.Helper()
	out := map[string]core.AuditEvent{}
	for _, e := range log.mustList(t, core.AuditQuery{Tenant: tenant}) {
		out[e.Action] = e
	}
	return out
}

// TestAuditAuth_LifecycleEvents covers the success path: signup auto
// sign-in, an explicit password sign-in, and sign-out each land in the
// tenant's audit trail with the actor and source IP recorded.
func TestAuditAuth_LifecycleEvents(t *testing.T) {
	h := newSignupHarness(t)
	log := NewMemAuditLog()
	h.gw.Audit = log

	// Signup auto-issues a session -> auth.signup.
	rw := rawDo(t, h, "POST", "/api/v1/auth/signup", signupBody("audit@example.com", "supersecret"))
	if rw.Code != http.StatusCreated {
		t.Fatalf("signup status=%d body=%s", rw.Code, rw.Body.String())
	}
	var su struct {
		Tenant string `json:"tenant"`
	}
	if err := json.Unmarshal(rw.Body.Bytes(), &su); err != nil {
		t.Fatalf("decode signup: %v", err)
	}

	// Explicit password sign-in -> auth.signin, returns a session token.
	rw = rawDo(t, h, "POST", "/api/v1/auth/signin", signupBody("audit@example.com", "supersecret"))
	if rw.Code != http.StatusOK {
		t.Fatalf("signin status=%d body=%s", rw.Code, rw.Body.String())
	}
	var si struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(rw.Body.Bytes(), &si); err != nil {
		t.Fatalf("decode signin: %v", err)
	}

	// Sign out the session we just got -> auth.signout.
	soReq := httptest.NewRequest("POST", "/api/v1/auth/signout", nil)
	soReq.Header.Set("Authorization", "Bearer "+si.Token)
	soRW := httptest.NewRecorder()
	ServeForTest(h.gw, soRW, soReq)
	if soRW.Code != http.StatusNoContent {
		t.Fatalf("signout status=%d", soRW.Code)
	}

	got := auditActions(t, log, su.Tenant)
	for _, action := range []string{"auth.signup", "auth.signin", "auth.signout"} {
		e, ok := got[action]
		if !ok {
			t.Errorf("missing audit event %q (have %v)", action, keysOf(got))
			continue
		}
		if e.Actor != "audit@example.com" {
			t.Errorf("%s actor = %q, want audit@example.com", action, e.Actor)
		}
		if !strings.Contains(e.Detail, "ip=") {
			t.Errorf("%s detail = %q, want it to record the source ip", action, e.Detail)
		}
	}
}

// TestAuditAuth_FailedSigninNoTenant verifies a wrong-password attempt is
// recorded as auth.signin_failed under the empty (platform-level) tenant —
// we don't resolve the tenant, since that would reveal whether the email
// maps to an account.
func TestAuditAuth_FailedSigninNoTenant(t *testing.T) {
	h := newSignupHarness(t)
	log := NewMemAuditLog()
	h.gw.Audit = log

	if rw := rawDo(t, h, "POST", "/api/v1/auth/signup", signupBody("real@example.com", "supersecret")); rw.Code != http.StatusCreated {
		t.Fatalf("signup status=%d", rw.Code)
	}

	rw := rawDo(t, h, "POST", "/api/v1/auth/signin", signupBody("real@example.com", "wrong-password"))
	if rw.Code != http.StatusUnauthorized {
		t.Fatalf("signin status=%d, want 401", rw.Code)
	}

	// The failure is recorded under the empty tenant, not the user's.
	failures := auditActions(t, log, "")
	e, ok := failures["auth.signin_failed"]
	if !ok {
		t.Fatalf("auth.signin_failed not recorded under platform-level tenant")
	}
	if e.Actor != "real@example.com" {
		t.Errorf("actor = %q, want the attempted email", e.Actor)
	}
}

func keysOf(m map[string]core.AuditEvent) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
