package daemon

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLandingAuthGate covers the optional marketing landing served
// alongside the SPA: GET / is gated on the session, marketing
// pages/assets serve publicly, and the SPA owns everything else.
func TestLandingAuthGate(t *testing.T) {
	h := newGatewayHarness(t)

	webDir := t.TempDir()
	mustWrite(t, filepath.Join(webDir, "index.html"), "SPA-APP-SHELL")
	mustWrite(t, filepath.Join(webDir, "assets", "app.js"), "console.log(1)")

	landingDir := t.TempDir()
	mustWrite(t, filepath.Join(landingDir, "landing.html"), "MARKETING-HOME")
	mustWrite(t, filepath.Join(landingDir, "style.css"), "body{}")
	mustWrite(t, filepath.Join(landingDir, "pricing", "index.html"), "PRICING-PAGE")

	h.gw.WebDist = webDir
	h.gw.LandingDir = landingDir

	get := func(path string, withSession bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if withSession {
			// A browser navigation carries the session cookie, not a
			// bearer header; the harness token authenticates either way.
			req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: h.token})
		}
		rw := httptest.NewRecorder()
		ServeForTest(h.gw, rw, req)
		return rw
	}

	t.Run("anonymous root serves landing", func(t *testing.T) {
		rw := get("/", false)
		if rw.Code != http.StatusOK || !strings.Contains(rw.Body.String(), "MARKETING-HOME") {
			t.Fatalf("anonymous / = %d %q, want landing", rw.Code, rw.Body.String())
		}
	})

	t.Run("signed-in root serves SPA", func(t *testing.T) {
		rw := get("/", true)
		if rw.Code != http.StatusOK || !strings.Contains(rw.Body.String(), "SPA-APP-SHELL") {
			t.Fatalf("signed-in / = %d %q, want SPA shell", rw.Code, rw.Body.String())
		}
	})

	t.Run("marketing page serves publicly", func(t *testing.T) {
		// Canonical directory URL serves the page directly...
		rw := get("/pricing/", false)
		if rw.Code != http.StatusOK || !strings.Contains(rw.Body.String(), "PRICING-PAGE") {
			t.Fatalf("/pricing/ = %d %q, want pricing page", rw.Code, rw.Body.String())
		}
		// ...and the no-slash form 301s to it (standard FileServer /
		// nginx directory-index behaviour; the browser follows it).
		rw = get("/pricing", false)
		if rw.Code != http.StatusMovedPermanently {
			t.Fatalf("/pricing = %d, want 301 redirect to /pricing/", rw.Code)
		}
	})

	t.Run("marketing asset serves publicly", func(t *testing.T) {
		rw := get("/style.css", false)
		if rw.Code != http.StatusOK || !strings.Contains(rw.Body.String(), "body{}") {
			t.Fatalf("/style.css = %d %q, want stylesheet", rw.Code, rw.Body.String())
		}
	})

	t.Run("SPA client route falls through to SPA", func(t *testing.T) {
		// /flows isn't a file under the landing dir, so it must resolve
		// to the SPA index.html for React Router to handle client-side.
		rw := get("/flows", false)
		if rw.Code != http.StatusOK || !strings.Contains(rw.Body.String(), "SPA-APP-SHELL") {
			t.Fatalf("/flows = %d %q, want SPA shell", rw.Code, rw.Body.String())
		}
	})

	t.Run("SPA asset still served", func(t *testing.T) {
		rw := get("/assets/app.js", false)
		if rw.Code != http.StatusOK || !strings.Contains(rw.Body.String(), "console.log(1)") {
			t.Fatalf("/assets/app.js = %d %q, want SPA asset", rw.Code, rw.Body.String())
		}
	})
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
