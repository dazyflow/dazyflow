package daemon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.sr.ht/~klahr/hazyflow/auth"
)

// newSignupHarness extends the default gateway harness with the
// Users + Sessions stores wired up and signup enabled, mirroring
// what hzd does when --signup is set.
func newSignupHarness(t *testing.T) *gatewayHarness {
	t.Helper()
	h := newGatewayHarness(t)
	h.gw.Users, _ = auth.OpenJSONUserStore("")
	h.gw.Sessions = auth.NewMemSessionStore()
	h.gw.EnableSignup = true
	// The default harness's Auth chain is API-key-only; extend it
	// with the session authenticator so the token returned from
	// signup actually validates on subsequent requests (like real
	// hzd, which always wires both authenticators).
	h.svc.Auth = auth.Chain{
		&auth.APIKeyAuthenticator{Store: h.ks},
		&auth.SessionAuthenticator{Store: h.gw.Sessions},
	}
	return h
}

// signupBody returns the JSON body for POST /signup.
func signupBody(email, password string) []byte {
	b, _ := json.Marshal(map[string]any{"email": email, "password": password})
	return b
}

// rawDo posts without a bearer token — signup is unauthenticated.
func rawDo(t *testing.T, h *gatewayHarness, method, path string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBuffer(body))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	return rw
}

func TestSignup_HappyPath(t *testing.T) {
	h := newSignupHarness(t)
	rw := rawDo(t, h, "POST", "/api/v1/auth/signup", signupBody("new@example.com", "supersecret"))
	if rw.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rw.Code, rw.Body.String())
	}
	var resp struct {
		Token   string `json:"token"`
		Subject string `json:"subject"`
		Tenant  string `json:"tenant"`
	}
	if err := json.Unmarshal(rw.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Token == "" {
		t.Error("token missing — signup should auto sign-in")
	}
	if resp.Subject != "new@example.com" {
		t.Errorf("subject = %q", resp.Subject)
	}
	if !strings.HasPrefix(resp.Tenant, "usr_") {
		t.Errorf("tenant = %q, want usr_<hex>", resp.Tenant)
	}

	// Verify the user landed in the store with the right roles.
	user, err := h.gw.Users.GetByEmail(t.Context(), "new@example.com")
	if err != nil {
		t.Fatalf("user not in store: %v", err)
	}
	if len(user.PasswordHash) == 0 {
		t.Error("password hash not stored")
	}
	if user.Workspace != "main" {
		t.Errorf("workspace = %q, want main", user.Workspace)
	}
	// Should be able to immediately use the returned token.
	whoamiReq := httptest.NewRequest("GET", "/api/v1/me", nil)
	whoamiReq.Header.Set("Authorization", "Bearer "+resp.Token)
	whoamiRW := httptest.NewRecorder()
	ServeForTest(h.gw, whoamiRW, whoamiReq)
	if whoamiRW.Code != http.StatusOK {
		t.Errorf("whoami with new session token failed: %d", whoamiRW.Code)
	}
}

func TestSignup_SetsSessionCookie(t *testing.T) {
	// The cookie is what the browser will rely on for subsequent
	// same-origin requests — it must be HttpOnly, SameSite=Lax.
	h := newSignupHarness(t)
	rw := rawDo(t, h, "POST", "/api/v1/auth/signup", signupBody("cookie@example.com", "supersecret"))
	if rw.Code != http.StatusCreated {
		t.Fatalf("status=%d", rw.Code)
	}
	set := rw.Result().Cookies()
	var session *http.Cookie
	for _, c := range set {
		if c.Name == sessionCookieName {
			session = c
			break
		}
	}
	if session == nil {
		t.Fatal("session cookie missing from signup response")
	}
	if !session.HttpOnly {
		t.Error("session cookie should be HttpOnly")
	}
	if session.SameSite != http.SameSiteLaxMode {
		t.Errorf("session cookie SameSite = %v, want Lax", session.SameSite)
	}
}

func TestSignup_DuplicateEmailRejected(t *testing.T) {
	h := newSignupHarness(t)
	// First signup succeeds.
	first := rawDo(t, h, "POST", "/api/v1/auth/signup", signupBody("dup@example.com", "supersecret"))
	if first.Code != http.StatusCreated {
		t.Fatalf("first: %d", first.Code)
	}
	// Second signup with same email — 409.
	second := rawDo(t, h, "POST", "/api/v1/auth/signup", signupBody("dup@example.com", "differentpass"))
	if second.Code != http.StatusConflict {
		t.Errorf("status=%d, want 409", second.Code)
	}
	// Critical: the original password still works after a duplicate
	// signup attempt — the duplicate must NOT silently overwrite.
	signinRW := rawDo(t, h, "POST", "/api/v1/auth/signin", signupBody("dup@example.com", "supersecret"))
	if signinRW.Code != http.StatusOK {
		t.Errorf("original password no longer works: %d", signinRW.Code)
	}
}

func TestSignup_TenantIDsAreUnique(t *testing.T) {
	// Two distinct signups must get distinct tenant IDs. With 4 random
	// bytes, collisions are vanishingly unlikely but we still sanity-
	// check the assignment.
	h := newSignupHarness(t)
	tenants := map[string]bool{}
	for i := 0; i < 10; i++ {
		email := fmt.Sprintf("user%d@example.com", i)
		rw := rawDo(t, h, "POST", "/api/v1/auth/signup", signupBody(email, "supersecret"))
		if rw.Code != http.StatusCreated {
			t.Fatalf("signup %d: %d", i, rw.Code)
		}
		var resp struct{ Tenant string }
		_ = json.Unmarshal(rw.Body.Bytes(), &resp)
		if tenants[resp.Tenant] {
			t.Errorf("tenant ID collision: %q", resp.Tenant)
		}
		tenants[resp.Tenant] = true
	}
}

func TestSignup_RejectsBadEmail(t *testing.T) {
	h := newSignupHarness(t)
	for _, bad := range []string{
		"",                      // empty
		"not-an-email",          // no @
		"a@b",                   // no dot in domain
		"@example.com",          // empty local
		"name@",                 // empty domain
		"name with space@x.com", // whitespace
		"name\r\n@x.com",        // control chars
	} {
		t.Run(bad, func(t *testing.T) {
			rw := rawDo(t, h, "POST", "/api/v1/auth/signup", signupBody(bad, "supersecret"))
			if rw.Code != http.StatusBadRequest {
				t.Errorf("email %q got status %d, want 400", bad, rw.Code)
			}
		})
	}
}

func TestSignup_RejectsShortPassword(t *testing.T) {
	h := newSignupHarness(t)
	rw := rawDo(t, h, "POST", "/api/v1/auth/signup", signupBody("short@example.com", "abc"))
	if rw.Code != http.StatusBadRequest {
		t.Errorf("status=%d, want 400", rw.Code)
	}
}

func TestSignup_RejectsLongPassword(t *testing.T) {
	h := newSignupHarness(t)
	huge := strings.Repeat("x", 300)
	rw := rawDo(t, h, "POST", "/api/v1/auth/signup", signupBody("long@example.com", huge))
	if rw.Code != http.StatusBadRequest {
		t.Errorf("status=%d, want 400", rw.Code)
	}
}

func TestSignup_EmailNormalized(t *testing.T) {
	// "  USER@EXAMPLE.COM " should land as "user@example.com" — both
	// for the stored user record AND the duplicate-detection path.
	h := newSignupHarness(t)
	rw := rawDo(t, h, "POST", "/api/v1/auth/signup", signupBody("  USER@EXAMPLE.COM ", "supersecret"))
	if rw.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rw.Code, rw.Body.String())
	}
	user, err := h.gw.Users.GetByEmail(t.Context(), "user@example.com")
	if err != nil {
		t.Fatalf("normalized lookup failed: %v", err)
	}
	if user.Email != "user@example.com" {
		t.Errorf("stored email = %q, want lowercased+trimmed", user.Email)
	}
}

func TestSignup_DisabledIs501(t *testing.T) {
	// Default deployment: EnableSignup=false → endpoint returns 501.
	h := newGatewayHarness(t)
	h.gw.Users, _ = auth.OpenJSONUserStore("")
	h.gw.Sessions = auth.NewMemSessionStore()
	// EnableSignup intentionally NOT set.
	rw := rawDo(t, h, "POST", "/api/v1/auth/signup", signupBody("any@example.com", "supersecret"))
	if rw.Code != http.StatusNotImplemented {
		t.Errorf("status=%d, want 501", rw.Code)
	}
}

func TestSignup_NoUsersStoreIs501(t *testing.T) {
	// Even with EnableSignup=true, missing Users/Sessions wiring
	// must surface as 501 rather than panicking.
	h := newGatewayHarness(t)
	h.gw.EnableSignup = true
	// No Users, no Sessions.
	rw := rawDo(t, h, "POST", "/api/v1/auth/signup", signupBody("any@example.com", "supersecret"))
	if rw.Code != http.StatusNotImplemented {
		t.Errorf("status=%d, want 501", rw.Code)
	}
}

func TestSignup_GrantsEditorAndTenantOwnerRoles(t *testing.T) {
	// New users need enough permissions to actually use the product
	// — graph editing, secret writes (OAuth flows), tenant admin
	// (issuing API keys later). Pin this so a future refactor of
	// defaultSignupRoles doesn't accidentally lock new users out.
	h := newSignupHarness(t)
	_ = rawDo(t, h, "POST", "/api/v1/auth/signup", signupBody("perms@example.com", "supersecret"))
	user, _ := h.gw.Users.GetByEmail(t.Context(), "perms@example.com")
	perms := map[string]bool{}
	for _, role := range user.Roles {
		for _, p := range role.Permissions {
			perms[string(p)] = true
		}
	}
	for _, required := range []string{
		"graph:run", "graph:edit", "graph:admin",
		"secret:read", "secret:write",
		"organization:admin",
	} {
		if !perms[required] {
			t.Errorf("missing permission %q in default signup roles", required)
		}
	}
}
