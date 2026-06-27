package core

import "testing"

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
	for _, p := range []Permission{PermGraphRun, PermGraphEdit, PermGraphAdmin, PermSecretRead, PermSecretWrite} {
		if !hasPerm(e.Permissions, p) {
			t.Errorf("editor missing %s", p)
		}
	}
	if hasPerm(e.Permissions, PermOrganizationAdmin) {
		t.Error("editor must not carry organization:admin")
	}

	a := TeamRoleAdmin()
	if a.Name != "admin" || !hasPerm(a.Permissions, PermOrganizationAdmin) || !hasPerm(a.Permissions, PermGraphEdit) {
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
