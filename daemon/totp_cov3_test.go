// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"net/http"
	"testing"
)

// Validation / error branches of the /me/totp handlers that the happy-path
// totp_test.go doesn't reach.

func TestTOTPConfirm_CodeRequired(t *testing.T) {
	h, u, pw := newTOTPHarness(t)
	token := sessionTokenFor(t, h, u.Email, pw)
	rw := bearerDo(t, h, token, "POST", "/api/v1/me/totp/confirm", map[string]string{"code": ""})
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("empty code = %d (%s), want 400", rw.Code, rw.Body.String())
	}
}

func TestTOTPConfirm_NoPendingEnrolment(t *testing.T) {
	h, u, pw := newTOTPHarness(t)
	token := sessionTokenFor(t, h, u.Email, pw)
	// No /setup first -> confirm has no pending secret -> 400 no_pending_enrolment.
	rw := bearerDo(t, h, token, "POST", "/api/v1/me/totp/confirm", map[string]string{"code": "123456"})
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("confirm w/o setup = %d (%s), want 400", rw.Code, rw.Body.String())
	}
}

func TestTOTPConfirm_DecodeError(t *testing.T) {
	h, u, pw := newTOTPHarness(t)
	token := sessionTokenFor(t, h, u.Email, pw)
	req := newRawReq(t, h, "POST", "/api/v1/me/totp/confirm", "{bad")
	req.Header.Set("Authorization", "Bearer "+token)
	rw := serveRaw(h, req)
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("malformed confirm = %d (%s), want 400", rw.Code, rw.Body.String())
	}
}

func TestTOTPDisable_PasswordRequired(t *testing.T) {
	h, u, pw := newTOTPHarness(t)
	token := sessionTokenFor(t, h, u.Email, pw)
	rw := bearerDo(t, h, token, "POST", "/api/v1/me/totp/disable", map[string]string{"password": ""})
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("disable w/o password = %d (%s), want 400", rw.Code, rw.Body.String())
	}
}

func TestTOTPDisable_BadPassword(t *testing.T) {
	h, u, pw := newTOTPHarness(t)
	token := sessionTokenFor(t, h, u.Email, pw)
	rw := bearerDo(t, h, token, "POST", "/api/v1/me/totp/disable", map[string]string{"password": "wrong"})
	if rw.Code != http.StatusUnauthorized {
		t.Fatalf("disable wrong password = %d (%s), want 401", rw.Code, rw.Body.String())
	}
}

func TestTOTPRegenerate_NotEnrolled(t *testing.T) {
	h, u, pw := newTOTPHarness(t)
	token := sessionTokenFor(t, h, u.Email, pw)
	// Never enrolled -> regenerate returns 400 totp_not_enrolled.
	rw := bearerDo(t, h, token, "POST", "/api/v1/me/totp/recovery-codes", nil)
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("regenerate not enrolled = %d (%s), want 400", rw.Code, rw.Body.String())
	}
}

func TestTOTPVerify_MissingChallenge(t *testing.T) {
	h, _, _ := newTOTPHarness(t)
	// No challenge token -> rejected (400/401).
	rw := bearerDo(t, h, "", "POST", "/api/v1/auth/totp", map[string]string{"code": "123456"})
	if rw.Code != http.StatusBadRequest && rw.Code != http.StatusUnauthorized {
		t.Fatalf("verify no challenge = %d (%s), want 400/401", rw.Code, rw.Body.String())
	}
}
