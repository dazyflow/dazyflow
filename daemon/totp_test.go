// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/auth"
	"git.sr.ht/~klahr/dazyflow/core"
	"github.com/pquerna/otp/totp"
)

// newTOTPHarness wires the password stores + a 32-byte TOTP key + the
// in-memory challenge store, then seeds one password user the test can
// enrol. Mirrors how cmd/dzd wires 2FA when DAZYFLOW_TOTP_KEY is set.
func newTOTPHarness(t *testing.T) (*gatewayHarness, auth.User, string) {
	t.Helper()
	h := newGatewayHarness(t)
	h.gw.Users, _ = auth.OpenJSONUserStore("")
	h.gw.Sessions = auth.NewMemSessionStore()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("rand: %v", err)
	}
	h.gw.TOTPKey = key
	h.gw.TOTPChallenges = auth.NewMemTOTPChallengeStore()
	// Extend the auth chain so a session token issued by sign-in / verify
	// validates on /me/totp calls, like real dzd.
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

// bearerDo issues an authenticated request with an arbitrary token (the
// harness's do() always uses the editor API key).
func bearerDo(t *testing.T, h *gatewayHarness, token, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf *bytes.Buffer
	if body != nil {
		b, _ := json.Marshal(body)
		buf = bytes.NewBuffer(b)
	} else {
		buf = bytes.NewBuffer(nil)
	}
	req := httptest.NewRequest(method, path, buf)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	return rw
}

// sessionTokenFor signs the seeded user in with their password (no 2FA
// yet) and returns the session token, so we can call the authenticated
// /me/totp enrolment endpoints.
func sessionTokenFor(t *testing.T, h *gatewayHarness, email, password string) string {
	t.Helper()
	rw := bearerDo(t, h, "", "POST", "/api/v1/auth/signin",
		map[string]string{"email": email, "password": password})
	if rw.Code != http.StatusOK {
		t.Fatalf("signin status=%d body=%s", rw.Code, rw.Body.String())
	}
	var resp struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(rw.Body.Bytes(), &resp)
	if resp.Token == "" {
		t.Fatalf("signin returned no token: %s", rw.Body.String())
	}
	return resp.Token
}

func TestTOTP_EnrolThenTwoLegSignin(t *testing.T) {
	h, u, pw := newTOTPHarness(t)
	token := sessionTokenFor(t, h, u.Email, pw)

	// Setup: pending secret + provisioning data.
	rw := bearerDo(t, h, token, "POST", "/api/v1/me/totp/setup", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("setup status=%d body=%s", rw.Code, rw.Body.String())
	}
	var setup struct {
		OTPAuthURL   string `json:"otp_auth_url"`
		SecretBase32 string `json:"secret_base32"`
		QRPNGDataURL string `json:"qr_png_data_url"`
	}
	if err := json.Unmarshal(rw.Body.Bytes(), &setup); err != nil {
		t.Fatalf("decode setup: %v", err)
	}
	if setup.SecretBase32 == "" || setup.QRPNGDataURL == "" {
		t.Fatalf("setup missing fields: %+v", setup)
	}

	// Confirm with a valid code → enabled + recovery codes returned once.
	code, _ := totp.GenerateCode(setup.SecretBase32, time.Now())
	rw = bearerDo(t, h, token, "POST", "/api/v1/me/totp/confirm", map[string]string{"code": code})
	if rw.Code != http.StatusOK {
		t.Fatalf("confirm status=%d body=%s", rw.Code, rw.Body.String())
	}
	var confirm struct {
		RecoveryCodes []string `json:"recovery_codes"`
	}
	_ = json.Unmarshal(rw.Body.Bytes(), &confirm)
	if len(confirm.RecoveryCodes) != 10 {
		t.Fatalf("got %d recovery codes, want 10", len(confirm.RecoveryCodes))
	}

	// Sign-in now returns a challenge instead of a session.
	rw = bearerDo(t, h, "", "POST", "/api/v1/auth/signin",
		map[string]string{"email": u.Email, "password": pw})
	if rw.Code != http.StatusOK {
		t.Fatalf("signin (2fa) status=%d body=%s", rw.Code, rw.Body.String())
	}
	var leg1 struct {
		TOTPRequired bool   `json:"totp_required"`
		Challenge    string `json:"challenge"`
		Token        string `json:"token"`
	}
	_ = json.Unmarshal(rw.Body.Bytes(), &leg1)
	if !leg1.TOTPRequired || leg1.Challenge == "" {
		t.Fatalf("expected totp challenge, got %s", rw.Body.String())
	}
	if leg1.Token != "" {
		t.Fatal("sign-in must not return a session token before the second factor")
	}

	// Leg 2 with a valid code → a real session.
	code2, _ := totp.GenerateCode(setup.SecretBase32, time.Now())
	rw = bearerDo(t, h, "", "POST", "/api/v1/auth/totp",
		map[string]string{"challenge": leg1.Challenge, "code": code2})
	if rw.Code != http.StatusOK {
		t.Fatalf("totp verify status=%d body=%s", rw.Code, rw.Body.String())
	}
	var leg2 struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(rw.Body.Bytes(), &leg2)
	if leg2.Token == "" {
		t.Fatal("totp verify returned no session token")
	}
	// The session token should authenticate /me/totp.
	rw = bearerDo(t, h, leg2.Token, "GET", "/api/v1/me/totp", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("me/totp with new session status=%d", rw.Code)
	}
}

func TestTOTP_VerifyRejectsBadCode(t *testing.T) {
	h, u, pw := newTOTPHarness(t)
	token := sessionTokenFor(t, h, u.Email, pw)

	rw := bearerDo(t, h, token, "POST", "/api/v1/me/totp/setup", nil)
	var setup struct {
		SecretBase32 string `json:"secret_base32"`
	}
	_ = json.Unmarshal(rw.Body.Bytes(), &setup)
	code, _ := totp.GenerateCode(setup.SecretBase32, time.Now())
	if rw := bearerDo(t, h, token, "POST", "/api/v1/me/totp/confirm", map[string]string{"code": code}); rw.Code != http.StatusOK {
		t.Fatalf("confirm failed: %s", rw.Body.String())
	}

	rw = bearerDo(t, h, "", "POST", "/api/v1/auth/signin",
		map[string]string{"email": u.Email, "password": pw})
	var leg1 struct {
		Challenge string `json:"challenge"`
	}
	_ = json.Unmarshal(rw.Body.Bytes(), &leg1)

	// Wrong code → 401, no session.
	rw = bearerDo(t, h, "", "POST", "/api/v1/auth/totp",
		map[string]string{"challenge": leg1.Challenge, "code": "000000"})
	if rw.Code != http.StatusUnauthorized {
		t.Fatalf("bad code status=%d body=%s, want 401", rw.Code, rw.Body.String())
	}
}

func TestTOTP_DisableRequiresPassword(t *testing.T) {
	h, u, pw := newTOTPHarness(t)
	token := sessionTokenFor(t, h, u.Email, pw)
	rw := bearerDo(t, h, token, "POST", "/api/v1/me/totp/setup", nil)
	var setup struct {
		SecretBase32 string `json:"secret_base32"`
	}
	_ = json.Unmarshal(rw.Body.Bytes(), &setup)
	code, _ := totp.GenerateCode(setup.SecretBase32, time.Now())
	if rw := bearerDo(t, h, token, "POST", "/api/v1/me/totp/confirm", map[string]string{"code": code}); rw.Code != http.StatusOK {
		t.Fatalf("confirm failed: %s", rw.Body.String())
	}

	// Wrong password → 401, 2FA stays on.
	if rw := bearerDo(t, h, token, "POST", "/api/v1/me/totp/disable", map[string]string{"password": "nope"}); rw.Code != http.StatusUnauthorized {
		t.Fatalf("disable w/ bad pw status=%d, want 401", rw.Code)
	}
	// Correct password → 204, 2FA off.
	if rw := bearerDo(t, h, token, "POST", "/api/v1/me/totp/disable", map[string]string{"password": pw}); rw.Code != http.StatusNoContent {
		t.Fatalf("disable status=%d body=%s, want 204", rw.Code, rw.Body.String())
	}
	// Sign-in is back to a one-step session.
	rw = bearerDo(t, h, "", "POST", "/api/v1/auth/signin",
		map[string]string{"email": u.Email, "password": pw})
	var resp struct {
		Token        string `json:"token"`
		TOTPRequired bool   `json:"totp_required"`
	}
	_ = json.Unmarshal(rw.Body.Bytes(), &resp)
	if resp.TOTPRequired || resp.Token == "" {
		t.Fatalf("after disable, sign-in should issue a session directly: %s", rw.Body.String())
	}
}

func TestTOTP_EndpointsAreOffWithoutKey(t *testing.T) {
	// A gateway with users but no TOTP key: the mutating endpoints 503.
	h := newGatewayHarness(t)
	h.gw.Users, _ = auth.OpenJSONUserStore("")
	h.gw.Sessions = auth.NewMemSessionStore()
	h.svc.Auth = auth.Chain{
		&auth.APIKeyAuthenticator{Store: h.ks},
		&auth.SessionAuthenticator{Store: h.gw.Sessions},
	}
	pw := "hunter2hunter2"
	hash, _ := auth.HashPassword(pw)
	_ = h.gw.Users.PutUser(t.Context(), auth.User{
		Email: "x@example.com", Subject: "x@example.com", PasswordHash: hash,
		Tenant: "t", Workspace: "ws",
	})
	token := sessionTokenFor(t, h, "x@example.com", pw)
	if rw := bearerDo(t, h, token, "POST", "/api/v1/me/totp/setup", nil); rw.Code != http.StatusServiceUnavailable {
		t.Fatalf("setup without key status=%d, want 503", rw.Code)
	}
}

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
