package daemon

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/auth"
)

var resetLinkRE = regexp.MustCompile(`https://app\.example/reset-password\?email=([^&\s]+)&token=([a-f0-9]{64})`)

// requestResetAndExtract drives forgot-password and pulls the reset link
// out of the captured email.
func requestResetAndExtract(t *testing.T, h *gatewayHarness, srv *fakeSMTP, email string) (string, string) {
	t.Helper()
	rw := h.do(t, "POST", "/api/v1/auth/forgot-password", map[string]string{"email": email})
	if rw.Code != http.StatusOK {
		t.Fatalf("forgot-password: %d %s", rw.Code, rw.Body.String())
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		// Most recent reset link (the account may also have a verification
		// + welcome mail in the captured stream).
		_, _, data, _ := srv.snapshot()
		ms := resetLinkRE.FindAllStringSubmatch(qpDecode(data), -1)
		if len(ms) > 0 {
			last := ms[len(ms)-1]
			got, err := url.QueryUnescape(last[1])
			if err != nil {
				t.Fatalf("bad email param %q: %v", last[1], err)
			}
			return got, last[2]
		}
		if time.Now().After(deadline) {
			t.Fatalf("no reset link in email:\n%s", data)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestPasswordReset_HappyPath: request → reset → old password dead, new
// password works, and all prior sessions are revoked.
func TestPasswordReset_HappyPath(t *testing.T) {
	h, _, srv := verificationHarness(t)

	// Create the account and keep its auto-issued session token.
	rw := h.do(t, "POST", "/api/v1/auth/signup", map[string]string{
		"email": "reset@example.com", "password": "OldPassw0rd!23",
	})
	if rw.Code != http.StatusCreated {
		t.Fatalf("signup: %d %s", rw.Code, rw.Body.String())
	}
	var signupResp struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(rw.Body.Bytes(), &signupResp); err != nil {
		t.Fatalf("decode signup resp: %v", err)
	}

	// The session works before the reset.
	if code := whoamiCode(t, h, signupResp.Token); code != http.StatusOK {
		t.Fatalf("pre-reset whoami: want 200, got %d", code)
	}

	_, token := requestResetAndExtract(t, h, srv, "reset@example.com")

	// Reset with the emailed token.
	if rw := h.do(t, "POST", "/api/v1/auth/reset-password", map[string]string{
		"email": "reset@example.com", "token": token, "password": "NewPassw0rd!99",
	}); rw.Code != http.StatusOK {
		t.Fatalf("reset-password: %d %s", rw.Code, rw.Body.String())
	}

	// Sign out everywhere: the old session is revoked.
	if code := whoamiCode(t, h, signupResp.Token); code == http.StatusOK {
		t.Fatalf("old session should be revoked after reset, got 200")
	}

	// Old password no longer works; new one does.
	if rw := h.do(t, "POST", "/api/v1/auth/signin", map[string]string{
		"email": "reset@example.com", "password": "OldPassw0rd!23",
	}); rw.Code == http.StatusOK {
		t.Fatalf("old password should be rejected after reset, got 200")
	}
	if rw := h.do(t, "POST", "/api/v1/auth/signin", map[string]string{
		"email": "reset@example.com", "password": "NewPassw0rd!99",
	}); rw.Code != http.StatusOK {
		t.Fatalf("new password sign-in: want 200, got %d %s", rw.Code, rw.Body.String())
	}

	// Single-use: the consumed token is dead.
	if rw := h.do(t, "POST", "/api/v1/auth/reset-password", map[string]string{
		"email": "reset@example.com", "token": token, "password": "Another!2345",
	}); rw.Code != http.StatusBadRequest {
		t.Fatalf("re-used reset token: want 400, got %d", rw.Code)
	}
}

// TestPasswordReset_NonEnumerating: forgot-password returns 200 for an
// address with no account, and never emails anything.
func TestPasswordReset_NonEnumerating(t *testing.T) {
	h, _, srv := verificationHarness(t)
	rw := h.do(t, "POST", "/api/v1/auth/forgot-password", map[string]string{
		"email": "ghost@example.com",
	})
	if rw.Code != http.StatusOK {
		t.Fatalf("forgot unknown email: want 200 (non-enumerating), got %d", rw.Code)
	}
	// Give a stray send a moment, then confirm nothing was mailed.
	time.Sleep(150 * time.Millisecond)
	if _, _, data, _ := srv.snapshot(); resetLinkRE.MatchString(qpDecode(data)) {
		t.Fatalf("a reset link was emailed for a non-existent account:\n%s", data)
	}
}

// TestPasswordReset_BadInputs: wrong email, garbage token, and a
// too-short new password are all rejected uniformly.
func TestPasswordReset_BadInputs(t *testing.T) {
	h, _, srv := verificationHarness(t)
	if rw := h.do(t, "POST", "/api/v1/auth/signup", map[string]string{
		"email": "bad@example.com", "password": "OldPassw0rd!23",
	}); rw.Code != http.StatusCreated {
		t.Fatalf("signup: %d %s", rw.Code, rw.Body.String())
	}
	_, token := requestResetAndExtract(t, h, srv, "bad@example.com")

	// Garbage token.
	if rw := h.do(t, "POST", "/api/v1/auth/reset-password", map[string]string{
		"email": "bad@example.com", "token": "deadbeef", "password": "NewPassw0rd!99",
	}); rw.Code != http.StatusBadRequest {
		t.Fatalf("garbage token: want 400, got %d", rw.Code)
	}
	// Valid token, unknown email.
	if rw := h.do(t, "POST", "/api/v1/auth/reset-password", map[string]string{
		"email": "nobody@example.com", "token": token, "password": "NewPassw0rd!99",
	}); rw.Code != http.StatusBadRequest {
		t.Fatalf("token with wrong email: want 400, got %d", rw.Code)
	}
	// Too-short password rejected — and the token must survive (not burned),
	// so a subsequent valid reset still works.
	if rw := h.do(t, "POST", "/api/v1/auth/reset-password", map[string]string{
		"email": "bad@example.com", "token": token, "password": "short",
	}); rw.Code != http.StatusBadRequest {
		t.Fatalf("short password: want 400, got %d", rw.Code)
	}
	if rw := h.do(t, "POST", "/api/v1/auth/reset-password", map[string]string{
		"email": "bad@example.com", "token": token, "password": "NewPassw0rd!99",
	}); rw.Code != http.StatusOK {
		t.Fatalf("valid reset after rejected short password: want 200, got %d %s", rw.Code, rw.Body.String())
	}
}

// TestWelcomeEmail_SentOnSignup: a welcome email goes out on signup. Use
// a mailer WITHOUT a public base URL so email verification is inactive —
// the welcome is then the only message, easy to assert.
func TestWelcomeEmail_SentOnSignup(t *testing.T) {
	h, _, srv := verificationHarness(t)
	h.svc.PublicBaseURL = "" // disables verification; welcome only needs the mailer

	if rw := h.do(t, "POST", "/api/v1/auth/signup", map[string]string{
		"email": "welcome@example.com", "password": "Passw0rd!2345",
	}); rw.Code != http.StatusCreated {
		t.Fatalf("signup: %d %s", rw.Code, rw.Body.String())
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		_, _, data, to := srv.snapshot()
		if strings.Contains(data, "Subject: Welcome to Dazyflow") && joinHas(to, "welcome@example.com") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no welcome email captured:\nto=%v\ndata=%s", to, data)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestPasswordReset_ExpiredToken: a token whose expiry has passed is
// rejected even though the hash matches.
func TestPasswordReset_ExpiredToken(t *testing.T) {
	h, users, _ := verificationHarness(t)
	token := "abc123def456" // plaintext the "email" would carry
	hash := sha256.Sum256([]byte(token))
	past := time.Now().Add(-time.Minute)
	pw, _ := auth.HashPassword("OldPassw0rd!23")
	if err := users.PutUser(t.Context(), auth.User{
		Email: "expired@example.com", Subject: "expired@example.com", Tenant: "t",
		PasswordHash: pw, ResetTokenHash: hash[:], ResetExpiresAt: &past,
	}); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if rw := h.do(t, "POST", "/api/v1/auth/reset-password", map[string]string{
		"email": "expired@example.com", "token": token, "password": "NewPassw0rd!99",
	}); rw.Code != http.StatusBadRequest {
		t.Fatalf("expired token: want 400, got %d %s", rw.Code, rw.Body.String())
	}
}

// TestPasswordReset_SSOAccountNoEmail: an account with no password (e.g.
// SSO-only) gets no reset email — reset is for password accounts.
func TestPasswordReset_SSOAccountNoEmail(t *testing.T) {
	h, users, srv := verificationHarness(t)
	if err := users.PutUser(t.Context(), auth.User{
		Email: "sso@example.com", Subject: "sso@example.com", Tenant: "t",
		// no PasswordHash
	}); err != nil {
		t.Fatalf("seed sso user: %v", err)
	}
	if rw := h.do(t, "POST", "/api/v1/auth/forgot-password", map[string]string{
		"email": "sso@example.com",
	}); rw.Code != http.StatusOK {
		t.Fatalf("forgot for sso: want 200, got %d", rw.Code)
	}
	time.Sleep(200 * time.Millisecond) // let any (erroneous) async send run
	if _, _, data, _ := srv.snapshot(); resetLinkRE.MatchString(qpDecode(data)) {
		t.Fatalf("a reset link was emailed to an SSO-only account:\n%s", data)
	}
}

// TestPasswordReset_MalformedBody: garbage JSON is a clean 400, not a panic.
func TestPasswordReset_MalformedBody(t *testing.T) {
	h, _, _ := verificationHarness(t)
	for _, path := range []string{"/api/v1/auth/forgot-password", "/api/v1/auth/reset-password"} {
		req := httptest.NewRequest("POST", path, bytes.NewBufferString("{not json"))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		ServeForTest(h.gw, rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s with garbage body: want 400, got %d", path, rec.Code)
		}
	}
}

// --- small test helpers ---

func whoamiCode(t *testing.T, h *gatewayHarness, token string) int {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	ServeForTest(h.gw, rec, req)
	return rec.Code
}

func joinHas(lines []string, want string) bool {
	for _, l := range lines {
		if strings.Contains(l, want) {
			return true
		}
	}
	return false
}
