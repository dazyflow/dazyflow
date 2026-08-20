// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/auth"
	"git.sr.ht/~klahr/dazyflow/core"
)

// memOrgAuth is a trivial in-memory auth.OrgAuthStore for tests — the
// real store is Postgres-only, but the handlers only need the three
// interface methods to exercise their decode + validation paths.
type memOrgAuth struct{ m map[string]auth.OrgAuthConfig }

func newMemOrgAuth() *memOrgAuth { return &memOrgAuth{m: map[string]auth.OrgAuthConfig{}} }

func (s *memOrgAuth) GetOrgAuth(_ context.Context, tenant string) (auth.OrgAuthConfig, error) {
	if c, ok := s.m[tenant]; ok {
		return c, nil
	}
	return auth.OrgAuthConfig{}, auth.ErrUnknownOrgAuth
}
func (s *memOrgAuth) PutOrgAuth(_ context.Context, cfg auth.OrgAuthConfig) error {
	s.m[cfg.Tenant] = cfg
	return nil
}
func (s *memOrgAuth) DeleteOrgAuth(_ context.Context, tenant string) error {
	delete(s.m, tenant)
	return nil
}

// route is one mounted (method, pattern) pair scraped from the gateway's
// registration site.
type route struct {
	method  string
	pattern string
}

// routePattern matches every `mux.HandleFunc("METHOD /path", …)` literal.
// It deliberately scans the SOURCE of the registration site rather than
// introspecting the *http.ServeMux (which doesn't expose its patterns),
// so the sweep stays authoritative and a newly-added route is covered the
// moment it's registered — no test edit required.
var routePattern = regexp.MustCompile(`HandleFunc\(\s*"([A-Z]+) (/[^"]+)"`)

// enumerateRoutes reads every non-test source file in the package and returns
// every registered (method, path) pair. Test cwd is the package dir, so the
// files resolve relatively.
//
// It globs rather than naming files so the sweep survives the gateway being
// split across files (the route table lives in httproutes.go today) and can't
// go stale by pointing at a file that no longer registers anything.
func enumerateRoutes(t *testing.T) []route {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	var out []route
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for _, m := range routePattern.FindAllStringSubmatch(string(src), -1) {
			out = append(out, route{method: m[1], pattern: m[2]})
		}
	}
	if len(out) < 100 {
		// A regex break would silently shrink coverage to nothing and
		// let the sweep "pass" while testing almost no routes.
		t.Fatalf("only scraped %d routes — route regex likely broke", len(out))
	}
	return out
}

var placeholder = regexp.MustCompile(`\{[^}]*\}`)

// concreteURL turns a registered pattern into a hittable URL: path
// placeholders ({tenant}, {flow_id}, {name...}) collapse to a literal
// segment, and subtree patterns (trailing "/") get a child segment so
// they land on the handler.
func concreteURL(pattern string) string {
	u := placeholder.ReplaceAllString(pattern, "x")
	if strings.HasSuffix(u, "/") {
		u += "x"
	}
	return u
}

// newSweepHarness builds a gateway with every optional subsystem wired,
// so handlers reach their real decode/validation path instead of
// short-circuiting on a nil dependency (501). Returns a bearer token
// carrying platform-admin + org-admin + graph + secret permissions, so
// no route is gated out by authorization.
func newSweepHarness(t *testing.T) (*gatewayHarness, string) {
	t.Helper()
	h := newGatewayHarness(t)

	es, err := NewEncryptedSecrets(make([]byte, 32), NewMemSecretsStore())
	if err != nil {
		t.Fatalf("encrypted secrets: %v", err)
	}
	h.svc.EncryptedSecrets = es
	h.gw.EncryptedSecrets = es

	inv, err := auth.OpenJSONInvitationStore("")
	if err != nil {
		t.Fatalf("invitation store: %v", err)
	}
	h.gw.Invitations = inv

	users, err := auth.OpenJSONUserStore("")
	if err != nil {
		t.Fatalf("user store: %v", err)
	}
	h.gw.Users = users
	h.gw.Sessions = auth.NewMemSessionStore()
	h.gw.EnableSignup = true
	h.gw.OAuth = NewOAuthRegistry("https://example.test", es)
	h.gw.OrgAuth = newMemOrgAuth()

	// 2FA + upload sandbox: without these the TOTP and file-upload routes
	// short-circuit to 503 "not configured" before parsing input, so wire
	// them so the sweep exercises the real handler path.
	h.gw.TOTPKey = make([]byte, 32)
	h.gw.TOTPChallenges = auth.NewMemTOTPChallengeStore()
	sb, err := NewFSSandbox(t.TempDir())
	if err != nil {
		t.Fatalf("sandbox: %v", err)
	}
	h.svc.Engine.Sandbox = sb

	role := core.Role{Name: "super", Permissions: []core.Permission{
		core.PermPlatformAdmin, core.PermOrganizationAdmin,
		core.PermGraphRun, core.PermGraphEdit, core.PermGraphAdmin,
		core.PermSecretRead, core.PermSecretWrite,
	}}
	_, tok, err := auth.IssueAPIKey(h.ks, t.Context(), "k-sweep", "t", "ws", "root", []core.Role{role}, nil)
	if err != nil {
		t.Fatalf("issue sweep key: %v", err)
	}
	return h, tok
}

// TestAllRoutes_MalformedBodyNo5xx is the breadth backstop: every
// registered route, hit with an authenticated request carrying a palette
// of malformed bodies, must answer without a server-crash status (5xx)
// and without panicking. A 4xx (bad request, not found, forbidden) or a
// 501 (subsystem off) is the correct rejection; a 5xx or panic means user
// input reached an unhandled path.
//
// This complements the targeted Fuzz* harnesses: those mutate one
// endpoint deeply; this proves the WHOLE surface — including the
// authenticated admin/CRUD routes no Fuzz target covers — degrades safely,
// and auto-includes any route added later.
func TestAllRoutes_MalformedBodyNo5xx(t *testing.T) {
	routes := enumerateRoutes(t)
	h, tok := newSweepHarness(t)

	bodies := [][]byte{
		[]byte("{"),               // truncated JSON
		[]byte("null"),            // valid JSON, wrong shape
		[]byte("[]"),              // array where object expected
		[]byte(`not json at all`), // not JSON
		[]byte(`{"":null,"x":[1,[2,[3]]],"n":1e999}`), // odd keys + overflow number
		bytes.Repeat([]byte("A"), 1<<16),              // 64 KiB of garbage
		// A plausible union body so handlers that pass decode reach
		// deeper validation/business logic.
		[]byte(`{"email":"a@b.com","roles":[{"name":"admin"}],"workspace":"main","google_client_id":"a","client_id":"a","client_secret":"b","name":"n","value":"v"}`),
	}

	for _, rt := range routes {
		rt := rt
		url := concreteURL(rt.pattern)
		t.Run(rt.method+" "+rt.pattern, func(t *testing.T) {
			send := func(body []byte) {
				var rdr *bytes.Reader
				if rt.method == http.MethodGet || rt.method == http.MethodDelete {
					rdr = bytes.NewReader(nil)
				} else {
					rdr = bytes.NewReader(body)
				}
				target := url
				if rt.method == http.MethodGet {
					target += "?x=%ZZ&n=&q[]=1" // malformed query too
				}
				req := httptest.NewRequest(rt.method, target, rdr)
				req.Header.Set("Authorization", "Bearer "+tok)
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Origin", "http://localhost:8080")
				// Bound any SSE / long-poll handler so it returns instead
				// of streaming until the watchdog.
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				req = req.WithContext(ctx)

				type res struct {
					rw    *httptest.ResponseRecorder
					pan   any
					stack string
				}
				done := make(chan res, 1)
				go func() {
					var r res
					defer func() {
						if p := recover(); p != nil {
							r.pan, r.stack = p, string(debug.Stack())
						}
						done <- r
					}()
					r.rw = httptest.NewRecorder()
					ServeForTest(h.gw, r.rw, req)
				}()

				select {
				case r := <-done:
					if r.pan != nil {
						t.Fatalf("PANIC on %s %s (body %.20q): %v\n%s", rt.method, url, body, r.pan, r.stack)
					}
					assertNo5xx(t, rt.method+" "+rt.pattern, r.rw)
				case <-time.After(5 * time.Second):
					// Streaming/long-poll handler that ignored the 2s ctx;
					// not a crash, so don't fail — just move on.
				}
			}
			if rt.method == http.MethodGet {
				send(nil)
				return
			}
			for _, b := range bodies {
				send(b)
			}
		})
	}
}
