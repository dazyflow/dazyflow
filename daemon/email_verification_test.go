package daemon

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/auth"
)

// verificationHarness: gateway + in-memory users/sessions + fake SMTP +
// public base URL, i.e. a verification-active deployment with signup on.
func verificationHarness(t *testing.T) (*gatewayHarness, *auth.JSONUserStore, *fakeSMTP) {
	t.Helper()
	h := newGatewayHarness(t)
	users, err := auth.OpenJSONUserStore("")
	if err != nil {
		t.Fatalf("open user store: %v", err)
	}
	srv := newFakeSMTP(t)
	mailer, err := NewMailerFromURL("smtp://"+srv.addr+"?tls=none", "noreply@example.com")
	if err != nil {
		t.Fatalf("mailer: %v", err)
	}
	h.gw.Users = users
	sessions := auth.NewMemSessionStore()
	h.gw.Sessions = sessions
	h.gw.EnableSignup = true
	// The harness's chain only knows API keys; verification flows issue
	// sessions, so add the session authenticator like dzd does.
	h.svc.Auth = auth.Chain{
		&auth.APIKeyAuthenticator{Store: h.ks},
		&auth.SessionAuthenticator{Store: sessions},
	}
	h.svc.Mailer = mailer
	h.svc.PublicBaseURL = "https://app.example"
	return h, users, srv
}

var verifyLinkRE = regexp.MustCompile(`https://app\.example/verify-email\?email=([^&\s]+)&token=([a-f0-9]{64})`)

// signupAndExtractLink runs a signup and pulls the link out of the
// captured email — the exact path a real user takes.
func signupAndExtractLink(t *testing.T, h *gatewayHarness, srv *fakeSMTP, email string) (string, string) {
	t.Helper()
	rw := h.do(t, "POST", "/api/v1/auth/signup", map[string]string{
		"email": email, "password": "TestPassw0rd!23",
	})
	if rw.Code != http.StatusCreated {
		t.Fatalf("signup: %d %s", rw.Code, rw.Body.String())
	}
	if !strings.Contains(rw.Body.String(), `"verification_email_sent":true`) {
		t.Fatalf("signup response should report the email went out: %s", rw.Body.String())
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		_, _, data, _ := srv.snapshot()
		if m := verifyLinkRE.FindStringSubmatch(data); m != nil {
			// The frontend reads the query param via URLSearchParams,
			// which percent-decodes — mirror that here.
			email, err := url.QueryUnescape(m[1])
			if err != nil {
				t.Fatalf("bad email param %q: %v", m[1], err)
			}
			return email, m[2]
		}
		if time.Now().After(deadline) {
			t.Fatalf("no verification link in email:\n%s", data)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestEmailVerification_FullLifecycle(t *testing.T) {
	h, users, srv := verificationHarness(t)
	emailParam, token := signupAndExtractLink(t, h, srv, "new@example.com")

	// Unverified: whoami (as that user) reports pending. The signup
	// session cookie path is exercised via the user record directly —
	// the gateway harness's do() uses an API-key token, so check the
	// store + the gate instead.
	u, err := users.GetByEmail(t.Context(), "new@example.com")
	if err != nil || u.EmailVerified() {
		t.Fatalf("fresh signup should be unverified: %+v / %v", u, err)
	}
	if len(u.VerifyTokenHash) == 0 || u.VerifyExpiresAt == nil {
		t.Fatalf("token not persisted: %+v", u)
	}

	// A wrong token bounces without flipping anything.
	rw := h.do(t, "POST", "/api/v1/auth/verify-email", map[string]string{
		"email": "new@example.com", "token": strings.Repeat("0", 64),
	})
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("wrong token: %d", rw.Code)
	}

	// The emailed link verifies.
	rw = h.do(t, "POST", "/api/v1/auth/verify-email", map[string]string{
		"email": emailParam, "token": token,
	})
	if rw.Code != http.StatusOK {
		t.Fatalf("verify: %d %s", rw.Code, rw.Body.String())
	}
	u, _ = users.GetByEmail(t.Context(), "new@example.com")
	if !u.EmailVerified() || len(u.VerifyTokenHash) != 0 || u.VerifyExpiresAt != nil {
		t.Fatalf("verify should set VerifiedAt and clear the token: %+v", u)
	}

	// Idempotent: re-clicking the consumed link still succeeds.
	rw = h.do(t, "POST", "/api/v1/auth/verify-email", map[string]string{
		"email": emailParam, "token": token,
	})
	if rw.Code != http.StatusOK {
		t.Fatalf("re-verify: %d", rw.Code)
	}
}

func TestEmailVerification_ExpiredToken(t *testing.T) {
	h, users, srv := verificationHarness(t)
	emailParam, token := signupAndExtractLink(t, h, srv, "late@example.com")

	u, _ := users.GetByEmail(t.Context(), "late@example.com")
	past := time.Now().Add(-time.Hour)
	u.VerifyExpiresAt = &past
	_ = users.PutUser(t.Context(), u)

	rw := h.do(t, "POST", "/api/v1/auth/verify-email", map[string]string{
		"email": emailParam, "token": token,
	})
	if rw.Code != http.StatusBadRequest || !strings.Contains(rw.Body.String(), "expired") {
		t.Fatalf("expired token: %d %s", rw.Code, rw.Body.String())
	}
}

func TestEmailVerification_UnknownEmailSameShape(t *testing.T) {
	// "No such account" answers exactly like "bad token" — no enumeration.
	h, _, _ := verificationHarness(t)
	rw := h.do(t, "POST", "/api/v1/auth/verify-email", map[string]string{
		"email": "ghost@example.com", "token": strings.Repeat("a", 64),
	})
	if rw.Code != http.StatusBadRequest || !strings.Contains(rw.Body.String(), "invalid or expired") {
		t.Fatalf("unknown email: %d %s", rw.Code, rw.Body.String())
	}
}

func TestEmailVerification_InactiveWithoutMailer(t *testing.T) {
	// No mailer: signup works exactly as before, nothing pending, no gate.
	h := newGatewayHarness(t)
	users, _ := auth.OpenJSONUserStore("")
	h.gw.Users = users
	h.gw.Sessions = auth.NewMemSessionStore()
	h.gw.EnableSignup = true

	rw := h.do(t, "POST", "/api/v1/auth/signup", map[string]string{
		"email": "plain@example.com", "password": "TestPassw0rd!23",
	})
	if rw.Code != http.StatusCreated || !strings.Contains(rw.Body.String(), `"verification_email_sent":false`) {
		t.Fatalf("signup without mailer: %d %s", rw.Code, rw.Body.String())
	}
	u, _ := users.GetByEmail(t.Context(), "plain@example.com")
	if len(u.VerifyTokenHash) != 0 {
		t.Errorf("no token should be minted without a mailer: %+v", u)
	}
	// Resend reports the feature off.
	if rw := h.do(t, "POST", "/api/v1/me/verification/resend", nil); rw.Code != http.StatusNotImplemented {
		t.Errorf("resend without mailer: %d, want 501", rw.Code)
	}
}

func TestEmailVerification_InviteGate(t *testing.T) {
	h, users, srv := verificationHarness(t)
	inv, err := auth.OpenJSONInvitationStore("")
	if err != nil {
		t.Fatalf("invitation store: %v", err)
	}
	h.gw.Invitations = inv

	// An unverified org admin (session-style principal) is blocked. The
	// harness's admin key has subject "root" (no @) — mint one whose
	// subject is a real email backed by an unverified user record.
	emailParam, token := signupAndExtractLink(t, h, srv, "owner@example.com")
	u, _ := users.GetByEmail(t.Context(), "owner@example.com")
	adminDoAs := func() *httptest.ResponseRecorder {
		sess, tok, err := auth.IssueSession(t.Context(), h.gw.Sessions, u, time.Hour)
		_ = sess
		if err != nil {
			t.Fatalf("issue session: %v", err)
		}
		req := httptest.NewRequest("POST", "/api/v1/admin/invitations",
			strings.NewReader(`{"email":"newcomer@example.com"}`))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		ServeForTest(h.gw, rec, req)
		return rec
	}
	if rw := adminDoAs(); rw.Code != http.StatusForbidden ||
		!strings.Contains(rw.Body.String(), "verify your email") {
		t.Fatalf("unverified inviter: %d %s", rw.Code, rw.Body.String())
	}

	// Verify, then the same invite goes through.
	if rw := h.do(t, "POST", "/api/v1/auth/verify-email", map[string]string{
		"email": emailParam, "token": token,
	}); rw.Code != http.StatusOK {
		t.Fatalf("verify: %d", rw.Code)
	}
	u, _ = users.GetByEmail(t.Context(), "owner@example.com")
	if rw := adminDoAs(); rw.Code != http.StatusCreated {
		t.Fatalf("verified inviter: %d %s", rw.Code, rw.Body.String())
	}
	// API-key principals (harness adminDo, subject "root") bypass the gate.
	if rw := h.adminDo(t, "POST", "/api/v1/admin/invitations",
		map[string]any{"email": "second@example.com"}); rw.Code != http.StatusCreated {
		t.Fatalf("api-key inviter: %d %s", rw.Code, rw.Body.String())
	}
}

func TestEmailVerification_Resend(t *testing.T) {
	h, users, srv := verificationHarness(t)
	_, oldToken := signupAndExtractLink(t, h, srv, "again@example.com")
	u, _ := users.GetByEmail(t.Context(), "again@example.com")

	// Resend as that user: a fresh token invalidates the old one.
	_, tok, err := auth.IssueSession(t.Context(), h.gw.Sessions, u, time.Hour)
	if err != nil {
		t.Fatalf("issue session: %v", err)
	}
	req := httptest.NewRequest("POST", "/api/v1/me/verification/resend", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	ServeForTest(h.gw, rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"sent":true`) {
		t.Fatalf("resend: %d %s", rec.Code, rec.Body.String())
	}
	if rw := h.do(t, "POST", "/api/v1/auth/verify-email", map[string]string{
		"email": "again@example.com", "token": oldToken,
	}); rw.Code != http.StatusBadRequest {
		t.Errorf("old token should be dead after resend: %d", rw.Code)
	}

	var newToken string
	deadline := time.Now().Add(2 * time.Second)
	for {
		_, _, data, _ := srv.snapshot()
		if m := verifyLinkRE.FindStringSubmatch(data); m != nil && m[2] != oldToken {
			newToken = m[2]
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no fresh link after resend")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if rw := h.do(t, "POST", "/api/v1/auth/verify-email", map[string]string{
		"email": "again@example.com", "token": newToken,
	}); rw.Code != http.StatusOK {
		t.Fatalf("fresh token: %d %s", rw.Code, rw.Body.String())
	}

	// Resend on a verified account says so without sending.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("POST", "/api/v1/me/verification/resend", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	ServeForTest(h.gw, rec, req)
	if !strings.Contains(rec.Body.String(), `"already_verified":true`) {
		t.Errorf("verified resend: %s", rec.Body.String())
	}
}

// TestEmailVerification_ResendRateLimited proves the resend route is behind
// the auth IP rate limiter (defense against token-churn / email spam): with a
// 1-request burst, the second resend in the window returns 429.
func TestEmailVerification_ResendRateLimited(t *testing.T) {
	h, users, srv := verificationHarness(t)
	signupAndExtractLink(t, h, srv, "rl@example.com")
	u, _ := users.GetByEmail(t.Context(), "rl@example.com")
	_, tok, err := auth.IssueSession(t.Context(), h.gw.Sessions, u, time.Hour)
	if err != nil {
		t.Fatalf("issue session: %v", err)
	}
	// Enable the limiter only now, so setup's signup (also rate-limited) didn't
	// consume the burst. 1/min, burst 1 → second resend in the window is 429.
	h.gw.AuthRateLimit = NewAuthRateLimiter(1, 1)
	resend := func() int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/v1/me/verification/resend", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		ServeForTest(h.gw, rec, req)
		return rec.Code
	}
	if got := resend(); got != http.StatusOK {
		t.Fatalf("first resend = %d, want 200", got)
	}
	if got := resend(); got != http.StatusTooManyRequests {
		t.Fatalf("second resend = %d, want 429 (rate limited)", got)
	}
}
