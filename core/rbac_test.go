// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"testing"
)

func hasPerm(perms []Permission, want Permission) bool {
	for _, p := range perms {
		if p == want {
			return true
		}
	}
	return false
}

func TestTeamRoleCatalog(t *testing.T) {
	v := TeamRoleViewer()
	if v.Name != "viewer" || !hasPerm(v.Permissions, PermGraphRun) || hasPerm(v.Permissions, PermGraphEdit) {
		t.Errorf("viewer wrong: %+v", v)
	}

	e := TeamRoleEditor()
	if e.Name != "editor" {
		t.Errorf("editor name: %q", e.Name)
	}
	for _, p := range []Permission{PermGraphRun, PermGraphEdit, PermSecretRead, PermSecretWrite} {
		if !hasPerm(e.Permissions, p) {
			t.Errorf("editor missing %s", p)
		}
	}
	if hasPerm(e.Permissions, PermOrganizationAdmin) {
		t.Error("editor must not carry organization:admin")
	}
	// graph:admin bypasses per-flow privacy; editor is the default invite role.
	if hasPerm(e.Permissions, PermGraphAdmin) {
		t.Error("editor must not carry graph:admin")
	}

	a := TeamRoleAdmin()
	if a.Name != "admin" || !hasPerm(a.Permissions, PermOrganizationAdmin) || !hasPerm(a.Permissions, PermGraphEdit) || !hasPerm(a.Permissions, PermGraphAdmin) {
		t.Errorf("admin wrong: %+v", a)
	}
	if hasPerm(a.Permissions, PermPlatformAdmin) {
		t.Error("admin must never carry platform:admin")
	}
}

// Constructors must return fresh values so a caller mutating one copy can't
// poison the catalog (the documented contract).
func TestTeamRoleAdmin_DoesNotPoisonEditor(t *testing.T) {
	_ = TeamRoleAdmin()
	e := TeamRoleEditor()
	if hasPerm(e.Permissions, PermOrganizationAdmin) {
		t.Error("TeamRoleAdmin leaked organization:admin into the editor catalog")
	}
}

func TestTeamRoleByName(t *testing.T) {
	tests := []struct {
		name   string
		want   string
		wantOK bool
	}{
		{"viewer", "viewer", true},
		{"editor", "editor", true},
		{"admin", "admin", true},
		{"custom", "", false},
		{"", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, ok := TeamRoleByName(tt.name)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if ok && r.Name != tt.want {
				t.Errorf("resolved name = %q, want %q", r.Name, tt.want)
			}
		})
	}
}

func TestCatalogEditorCannotSeeOthersPrivateFlow(t *testing.T) {
	priv := Graph{ID: "g", Tenant: "acme", Owner: "alice", Visibility: VisibilityPrivate}
	bob := Principal{Subject: "bob", Tenant: "acme", Roles: []Role{TeamRoleEditor()}}
	if IsFlowAdminPrincipal(bob) {
		t.Fatal("catalog editor must not be a flow admin")
	}
	if err := AuthorizeGraphView(bob, priv); err == nil {
		t.Error("editor viewed another member's private flow")
	}
	if err := AuthorizeGraphEdit(bob, priv); err == nil {
		t.Error("editor edited another member's owned flow")
	}
	adm := Principal{Subject: "carol", Tenant: "acme", Roles: []Role{TeamRoleAdmin()}}
	if err := AuthorizeGraphView(adm, priv); err != nil {
		t.Errorf("admin should view private flow: %v", err)
	}
}

func TestUpgradeLegacyRoles(t *testing.T) {
	legacy := Role{Name: "editor", Permissions: []Permission{PermSecretWrite, PermGraphAdmin, PermGraphRun, PermGraphEdit, PermSecretRead}}
	custom := Role{Name: "editor", Permissions: []Permission{PermGraphRun, PermGraphAdmin}}
	in := []Role{legacy, custom}
	out := UpgradeLegacyRoles(in)
	if out[0].Has(PermGraphAdmin) || !out[0].Has(PermGraphEdit) {
		t.Errorf("legacy editor not upgraded: %+v", out[0])
	}
	if !out[1].Has(PermGraphAdmin) {
		t.Error("custom role must be kept verbatim")
	}
	if got := UpgradeLegacyRoles([]Role{TeamRoleAdmin()}); !got[0].Has(PermGraphAdmin) {
		t.Error("admin must keep graph:admin")
	}
	if !in[0].Has(PermGraphAdmin) {
		t.Error("input slice must not be mutated")
	}
	owner := []Role{legacy, {Name: "tenant_owner", Permissions: []Permission{PermOrganizationAdmin}}}
	if got := UpgradeLegacyRoles(owner); !got[0].Has(PermGraphAdmin) {
		t.Error("a legacy signup owner must keep graph:admin")
	}
	plain := []Role{TeamRoleViewer()}
	if got := UpgradeLegacyRoles(plain); &got[0] != &plain[0] {
		t.Error("unchanged roles should be returned as-is")
	}
}
