// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dazyflow/dazyflow/core"
)

func TestResolveTenantScope(t *testing.T) {
	orgAdmin := []core.Role{{Name: "o", Permissions: []core.Permission{core.PermOrganizationAdmin}}}
	platform := []core.Role{{Name: "p", Permissions: []core.Permission{core.PermPlatformAdmin}}}
	cases := []struct {
		name       string
		p          core.Principal
		query      string
		wantTenant string
		wantStatus int
	}{
		{"own tenant by default", core.Principal{Tenant: "acme", Roles: orgAdmin}, "", "acme", 0},
		{"own tenant explicit", core.Principal{Tenant: "acme", Roles: orgAdmin}, "acme", "acme", 0},
		{"other tenant forbidden", core.Principal{Tenant: "acme", Roles: orgAdmin}, "globex", "", http.StatusForbidden},
		{"unbound non-admin forbidden", core.Principal{Roles: orgAdmin}, "globex", "", http.StatusForbidden},
		{"unbound no tenant", core.Principal{Roles: orgAdmin}, "", "", http.StatusBadRequest},
		{"platform admin crosses tenants", core.Principal{Tenant: "acme", Roles: platform}, "globex", "globex", 0},
		{"unbound platform admin", core.Principal{Roles: platform}, "globex", "globex", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			url := "/x"
			if tc.query != "" {
				url += "?tenant=" + tc.query
			}
			rw := httptest.NewRecorder()
			got, ok := resolveTenantScope(rw, httptest.NewRequest(http.MethodGet, url, nil), tc.p, "nope")
			if tc.wantStatus != 0 {
				if ok || rw.Code != tc.wantStatus {
					t.Fatalf("ok=%v code=%d, want rejection with %d", ok, rw.Code, tc.wantStatus)
				}
				return
			}
			if !ok || got != tc.wantTenant {
				t.Fatalf("got (%q, %v), want (%q, true); body=%s", got, ok, tc.wantTenant, rw.Body)
			}
		})
	}
}
