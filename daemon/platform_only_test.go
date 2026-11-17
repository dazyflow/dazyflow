// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
)

var platformOnlyRoute = regexp.MustCompile(`HandleFunc\(\s*"([A-Z]+) (/[^"]+)",\s*h\.requireAuth\(platformOnly\(`)

// The platform:admin gate lives only in platformOnly, applied in httproutes.go,
// so the handlers don't re-check. This pins both halves: every platform route
// is wrapped, and every wrapped route turns away an org admin who isn't a
// platform admin.
func TestPlatformOnlyRoutes_RejectNonPlatformAdmin(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile("httproutes.go")
	if err != nil {
		t.Fatal(err)
	}
	// Platform-only routes that live outside /admin/platform/.
	outsidePrefix := []string{
		"/api/v1/admin/version\"",
		"/api/v1/admin/system/log\"",
		"/api/v1/admin/oauth-providers",
		"/api/v1/admin/signup-invites",
		"/api/v1/admin/smtp-test\"",
	}
	for _, line := range strings.Split(string(src), "\n") {
		platform := strings.Contains(line, "/api/v1/admin/platform/")
		for _, p := range outsidePrefix {
			platform = platform || strings.Contains(line, p)
		}
		if platform && !strings.Contains(line, "platformOnly(") {
			t.Errorf("platform route not wrapped in platformOnly: %s", strings.TrimSpace(line))
		}
	}
	routes := platformOnlyRoute.FindAllStringSubmatch(string(src), -1)
	if len(routes) < 30 {
		t.Fatalf("only found %d platformOnly routes — regex likely broke", len(routes))
	}

	h := newGatewayHarness(t)
	role := core.Role{Name: "admin", Permissions: []core.Permission{core.PermOrganizationAdmin}}
	_, tok, err := auth.IssueAPIKey(h.ks, t.Context(), "k-orgadmin", "t", "ws", "orgadmin", []core.Role{role}, nil)
	if err != nil {
		t.Fatalf("issue key: %v", err)
	}
	param := regexp.MustCompile(`\{[^}]+\}`)
	for _, m := range routes {
		method, path := m[1], param.ReplaceAllString(m[2], "x")
		req := httptest.NewRequest(method, path, bytes.NewBufferString("{}"))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		rw := httptest.NewRecorder()
		ServeForTest(h.gw, rw, req)
		if rw.Code != http.StatusForbidden {
			t.Errorf("%s %s as org admin = %d, want 403; body=%s", method, path, rw.Code, rw.Body.String())
		}
	}
}
