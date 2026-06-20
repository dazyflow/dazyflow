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
