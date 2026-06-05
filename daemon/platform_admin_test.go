package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.sr.ht/~klahr/hazyflow/auth"
	"git.sr.ht/~klahr/hazyflow/core"
)

func hasPlatformAdmin(roles []core.Role) bool {
	for _, r := range roles {
		if r.Has(core.PermPlatformAdmin) {
			return true
		}
	}
	return false
}

func TestElevatePlatformAdmin(t *testing.T) {
	gw := &HTTPGateway{PlatformAdmins: []string{"boss@example.com"}}

	t.Run("listed email gets the role", func(t *testing.T) {
		got := gw.elevatePlatformAdmin(auth.User{Email: "boss@example.com"})
		if !hasPlatformAdmin(got.Roles) {
			t.Fatalf("expected platform:admin, got roles %+v", got.Roles)
		}
	})

	t.Run("match is case-insensitive and trims", func(t *testing.T) {
		got := gw.elevatePlatformAdmin(auth.User{Email: "  BOSS@Example.com "})
		if !hasPlatformAdmin(got.Roles) {
			t.Fatalf("expected platform:admin for normalized email, got %+v", got.Roles)
		}
	})

	t.Run("unlisted email is untouched", func(t *testing.T) {
		got := gw.elevatePlatformAdmin(auth.User{Email: "rando@example.com"})
		if hasPlatformAdmin(got.Roles) {
			t.Fatal("unlisted email must not get platform:admin")
		}
	})

	t.Run("empty allowlist grants nothing", func(t *testing.T) {
		bare := &HTTPGateway{}
		got := bare.elevatePlatformAdmin(auth.User{Email: "boss@example.com"})
		if hasPlatformAdmin(got.Roles) {
			t.Fatal("no allowlist must mean no platform admins")
		}
	})

	t.Run("does not duplicate or mutate the caller's slice", func(t *testing.T) {
		orig := []core.Role{{Name: "editor", Permissions: []core.Permission{core.PermGraphRun}}}
		got := gw.elevatePlatformAdmin(auth.User{Email: "boss@example.com", Roles: orig})
		if len(orig) != 1 {
			t.Fatalf("caller's slice was mutated: len=%d", len(orig))
		}
		if len(got.Roles) != 2 {
			t.Fatalf("expected editor + platform_admin, got %+v", got.Roles)
		}
	})

	t.Run("already platform admin is left alone", func(t *testing.T) {
		in := auth.User{Email: "boss@example.com", Roles: []core.Role{
			{Name: "x", Permissions: []core.Permission{core.PermPlatformAdmin}},
		}}
		got := gw.elevatePlatformAdmin(in)
		if len(got.Roles) != 1 {
			t.Fatalf("should not append a second platform-admin role, got %+v", got.Roles)
		}
	})
}

// TestSignup_ElevatesPlatformAdmin proves the allowlist is actually wired
// into the sign-in path end to end: a listed user's session reaches the
// platform-admin-gated admin endpoint, an unlisted one is forbidden.
func TestSignup_ElevatesPlatformAdmin(t *testing.T) {
	h := newSignupHarness(t)
	h.gw.PlatformAdmins = []string{"boss@example.com"}

	tokenFor := func(email string) string {
		rw := rawDo(t, h, "POST", "/api/v1/auth/signup", signupBody(email, "supersecret"))
		if rw.Code != http.StatusCreated {
			t.Fatalf("signup %s: status=%d body=%s", email, rw.Code, rw.Body.String())
		}
		var resp struct {
			Token string `json:"token"`
		}
		if err := json.Unmarshal(rw.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return resp.Token
	}

	// The OAuth registry is nil in this harness, so a principal that
	// clears requirePlatformAdmin reaches the 501 ("not configured")
	// branch; one that doesn't is rejected with 403 first.
	adminStatus := func(token string) int {
		req := httptest.NewRequest("GET", "/api/v1/admin/oauth-providers", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rw := httptest.NewRecorder()
		ServeForTest(h.gw, rw, req)
		return rw.Code
	}

	if got := adminStatus(tokenFor("boss@example.com")); got != http.StatusNotImplemented {
		t.Errorf("platform admin should pass the gate (expect 501 OAuth-not-configured), got %d", got)
	}
	if got := adminStatus(tokenFor("peon@example.com")); got != http.StatusForbidden {
		t.Errorf("non-admin should be 403 at the gate, got %d", got)
	}
}

// TestSignup_AllowlistBypassesDisabledSignup proves the bootstrap hatch:
// with self-serve signup OFF, an email in HAZYFLOW_PLATFORM_ADMINS can
// still create its account (so a fresh instance can mint its first
// super-admin without toggling EnableSignup), while everyone else still
// gets the 501. And the bypass is self-limiting — a second attempt for
// the same listed email is rejected as a duplicate, not silently reused.
func TestSignup_AllowlistBypassesDisabledSignup(t *testing.T) {
	h := newSignupHarness(t)
	h.gw.EnableSignup = false // signup is closed for the world
	h.gw.PlatformAdmins = []string{"boss@example.com"}

	t.Run("listed email may sign up while signup is disabled", func(t *testing.T) {
		rw := rawDo(t, h, "POST", "/api/v1/auth/signup", signupBody("boss@example.com", "supersecret"))
		if rw.Code != http.StatusCreated {
			t.Fatalf("status=%d body=%s, want 201", rw.Code, rw.Body.String())
		}
	})

	t.Run("second attempt for the same email is a duplicate, not a reuse", func(t *testing.T) {
		rw := rawDo(t, h, "POST", "/api/v1/auth/signup", signupBody("boss@example.com", "supersecret"))
		if rw.Code != http.StatusConflict {
			t.Errorf("status=%d, want 409 — the bypass must close after first claim", rw.Code)
		}
	})

	t.Run("unlisted email still gets 501", func(t *testing.T) {
		rw := rawDo(t, h, "POST", "/api/v1/auth/signup", signupBody("peon@example.com", "supersecret"))
		if rw.Code != http.StatusNotImplemented {
			t.Errorf("status=%d, want 501 — signup stays closed for non-admins", rw.Code)
		}
	})
}
