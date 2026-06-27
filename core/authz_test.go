// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"errors"
	"testing"
)

var (
	roleRunner = Role{Name: "runner", Permissions: []Permission{PermGraphRun}}
	roleAdmin  = Role{Name: "admin", Permissions: []Permission{PermOrganizationAdmin, PermGraphRun, PermGraphEdit}}
)

func TestRequire(t *testing.T) {
	p := Principal{Tenant: "t", Roles: []Role{roleRunner}}
	if err := Require(p, PermGraphRun); err != nil {
		t.Errorf("graph:run should pass: %v", err)
	}
	if err := Require(p, PermGraphEdit); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("graph:edit should fail with ErrUnauthorized: %v", err)
	}
}

func TestRequireTenant(t *testing.T) {
	p := Principal{Tenant: "acme"}
	if err := RequireTenant(p, "acme"); err != nil {
		t.Errorf("matching tenant should pass: %v", err)
	}
	if err := RequireTenant(p, "other"); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("cross-tenant should be unauthorized: %v", err)
	}
	empty := Principal{}
	if err := RequireTenant(empty, "acme"); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("empty tenant should be unauthorized")
	}
}

func TestRequireWorkspace_TenantAdminBypassesWorkspace(t *testing.T) {
	p := Principal{Tenant: "acme", Workspace: "other-ws", Roles: []Role{roleAdmin}}
	if err := RequireWorkspace(p, "acme", "team-a"); err != nil {
		t.Errorf("tenant admin should bypass workspace scope: %v", err)
	}
}

// Workspace is no longer an authorization dimension (one workspace per
// org), so RequireWorkspace decides purely on the tenant: a differing
// workspace within the same tenant passes, while a cross-tenant request
// still fails.
func TestRequireWorkspace_WorkspaceNotAnAuthzDimension(t *testing.T) {
	p := Principal{Tenant: "acme", Workspace: "ws1", Roles: []Role{roleRunner}}
	if err := RequireWorkspace(p, "acme", "ws2"); err != nil {
		t.Errorf("same-tenant access must not depend on workspace: %v", err)
	}
	if err := RequireWorkspace(p, "other", "ws1"); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("cross-tenant access should still fail: %v", err)
	}
}

func TestAuthorizeGraphRun(t *testing.T) {
	p := Principal{Tenant: "acme", Workspace: "ws1", Roles: []Role{roleRunner}}
	g := Graph{Tenant: "acme", Workspace: "ws1"}
	if err := AuthorizeGraphRun(p, g); err != nil {
		t.Errorf("expected pass: %v", err)
	}
	g.Tenant = "other"
	if err := AuthorizeGraphRun(p, g); !errors.Is(err, ErrUnauthorized) {
		t.Error("cross-tenant graph run should fail")
	}
}

func TestPermissions_FlattensRoles(t *testing.T) {
	p := Principal{Roles: []Role{
		{Name: "a", Permissions: []Permission{PermGraphRun, PermGraphEdit}},
		{Name: "b", Permissions: []Permission{PermGraphEdit, PermSecretRead}},
	}}
	got := p.Permissions()
	for _, want := range []Permission{PermGraphRun, PermGraphEdit, PermSecretRead} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing permission %s in flattened set", want)
		}
	}
	if len(got) != 3 {
		t.Errorf("expected 3 distinct perms, got %d: %v", len(got), got)
	}
	if len(Principal{}.Permissions()) != 0 {
		t.Error("empty principal should flatten to empty set")
	}
}

func TestCanAdminOrg_Cov(t *testing.T) {
	tests := []struct {
		name  string
		roles []Role
		want  bool
	}{
		{"org admin", []Role{{Permissions: []Permission{PermOrganizationAdmin}}}, true},
		{"platform admin", []Role{{Permissions: []Permission{PermPlatformAdmin}}}, true},
		{"plain runner", []Role{{Permissions: []Permission{PermGraphRun}}}, false},
		{"no roles", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CanAdminOrg(Principal{Roles: tt.roles}); got != tt.want {
				t.Errorf("CanAdminOrg = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRequireTenant_PlatformAdminCrosses(t *testing.T) {
	// Platform admin with no tenant of its own may still address any tenant.
	p := Principal{Roles: []Role{{Permissions: []Permission{PermPlatformAdmin}}}}
	if err := RequireTenant(p, "any-tenant"); err != nil {
		t.Errorf("platform admin should cross tenant boundaries: %v", err)
	}
	// Empty requested tenant with a tenant-bearing principal passes.
	if err := RequireTenant(Principal{Tenant: "acme"}, ""); err != nil {
		t.Errorf("empty requested tenant should pass: %v", err)
	}
}

func TestAuthorizeGraphView_Cov(t *testing.T) {
	owner := Principal{Subject: "u1", Tenant: "acme", Roles: []Role{{Permissions: []Permission{PermGraphRun}}}}
	stranger := Principal{Subject: "u2", Tenant: "acme", Roles: []Role{{Permissions: []Permission{PermGraphRun}}}}
	admin := Principal{Subject: "u3", Tenant: "acme", Roles: []Role{{Permissions: []Permission{PermOrganizationAdmin}}}}

	orgFlow := Graph{Tenant: "acme", Visibility: VisibilityOrg, Owner: "u1"}
	if err := AuthorizeGraphView(stranger, orgFlow); err != nil {
		t.Errorf("org-visible flow should be viewable by any tenant member: %v", err)
	}

	privFlow := Graph{ID: "g1", Tenant: "acme", Visibility: VisibilityPrivate, Owner: "u1"}
	if err := AuthorizeGraphView(owner, privFlow); err != nil {
		t.Errorf("owner should view their private flow: %v", err)
	}
	if err := AuthorizeGraphView(admin, privFlow); err != nil {
		t.Errorf("admin should view private flow: %v", err)
	}
	if err := AuthorizeGraphView(stranger, privFlow); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("stranger should not view private flow: %v", err)
	}

	// Cross-tenant fails before visibility.
	if err := AuthorizeGraphView(stranger, Graph{Tenant: "other"}); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("cross-tenant view should fail: %v", err)
	}

	// Legacy private flow with empty Owner reads as org-visible.
	legacy := Graph{Tenant: "acme", Visibility: VisibilityPrivate}
	if err := AuthorizeGraphView(stranger, legacy); err != nil {
		t.Errorf("legacy ownerless private flow should be viewable: %v", err)
	}
}

func TestAuthorizeGraphEdit_Cov(t *testing.T) {
	editorRole := Role{Permissions: []Permission{PermGraphEdit}}
	owner := Principal{Subject: "u1", Tenant: "acme", Roles: []Role{editorRole}}
	stranger := Principal{Subject: "u2", Tenant: "acme", Roles: []Role{editorRole}}
	admin := Principal{Subject: "u3", Tenant: "acme", Roles: []Role{{Permissions: []Permission{PermGraphEdit, PermGraphAdmin}}}}
	viewer := Principal{Subject: "u4", Tenant: "acme", Roles: []Role{{Permissions: []Permission{PermGraphRun}}}}

	// New flow (no owner): any editor may save.
	if err := AuthorizeGraphEdit(owner, Graph{Tenant: "acme"}); err != nil {
		t.Errorf("editor should save a new, ownerless flow: %v", err)
	}
	// Lacking graph:edit fails.
	if err := AuthorizeGraphEdit(viewer, Graph{Tenant: "acme"}); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("viewer without graph:edit should fail: %v", err)
	}
	// Owned flow: owner and admin may edit, stranger may not.
	owned := Graph{ID: "g9", Tenant: "acme", Owner: "u1"}
	if err := AuthorizeGraphEdit(owner, owned); err != nil {
		t.Errorf("owner should edit own flow: %v", err)
	}
	if err := AuthorizeGraphEdit(admin, owned); err != nil {
		t.Errorf("admin should edit owned flow: %v", err)
	}
	if err := AuthorizeGraphEdit(stranger, owned); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("non-owner editor should not edit owned flow: %v", err)
	}
	// Cross-tenant fails first.
	if err := AuthorizeGraphEdit(owner, Graph{Tenant: "other"}); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("cross-tenant edit should fail: %v", err)
	}
}

func TestIsFlowAdminPrincipal_Cov(t *testing.T) {
	if !IsFlowAdminPrincipal(Principal{Roles: []Role{{Permissions: []Permission{PermGraphAdmin}}}}) {
		t.Error("graph:admin should be a flow admin")
	}
	if !IsFlowAdminPrincipal(Principal{Roles: []Role{{Permissions: []Permission{PermOrganizationAdmin}}}}) {
		t.Error("organization:admin should be a flow admin")
	}
	if IsFlowAdminPrincipal(Principal{Roles: []Role{{Permissions: []Permission{PermGraphRun}}}}) {
		t.Error("plain runner should not be a flow admin")
	}
}
