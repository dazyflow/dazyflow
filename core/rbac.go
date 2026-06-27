// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

type Permission string

const (
	PermGraphRun          Permission = "graph:run"
	PermGraphEdit         Permission = "graph:edit"
	PermGraphAdmin        Permission = "graph:admin"
	PermModuleRegister    Permission = "module:register"
	PermSecretRead        Permission = "secret:read"
	PermSecretWrite       Permission = "secret:write"
	PermOrganizationAdmin Permission = "organization:admin"
	// PermPlatformAdmin is the cross-tenant super-admin role. Carriers
	// can see and act on every tenant on the dzd instance — manage
	// keys, list runs, issue keys in any tenant, etc. Distinct from
	// organization:admin (which is per-tenant). For SaaS-style hosting
	// where the operator runs dzd for many customer orgs.
	PermPlatformAdmin Permission = "platform:admin"
)

type Role struct {
	Name        string       `json:"name"`
	Permissions []Permission `json:"permissions"`
}

// PlatformAdminRole is the cross-tenant super-admin role stamped onto a session
// for an email in the DAZYFLOW_PLATFORM_ADMINS allowlist or carrying a runtime
// platform-admin grant. Kept here as one source of truth so the env-allowlist
// elevation and the grant store construct the identical role.
func PlatformAdminRole() Role {
	return Role{Name: "platform_admin", Permissions: []Permission{PermPlatformAdmin}}
}

func (r Role) Has(p Permission) bool {
	for _, have := range r.Permissions {
		if have == p {
			return true
		}
	}
	return false
}

// Team role catalog — the canonical viewer / editor / admin split used
// by invitations and the members page. One source of truth so signup,
// invites, and role changes can't drift apart (they used to: "viewer"
// existed for API keys and OIDC but not for invites). Constructors
// return fresh values so a caller mutating its copy can't poison the
// catalog.

// TeamRoleViewer can watch and run flows but change nothing: no graph
// edits, no secrets, no org admin.
func TeamRoleViewer() Role {
	return Role{Name: "viewer", Permissions: []Permission{PermGraphRun}}
}

// TeamRoleEditor does day-to-day graph work: build, run, recover flows
// and manage the secrets they need. No org administration.
func TeamRoleEditor() Role {
	return Role{Name: "editor", Permissions: []Permission{
		PermGraphRun, PermGraphEdit, PermGraphAdmin,
		PermSecretRead, PermSecretWrite,
	}}
}

// TeamRoleAdmin is an editor who can also manage the organization
// (members, invitations, API keys, org settings). Deliberately NOT
// platform:admin — that's the cross-tenant operator role and is never
// part of the team catalog.
func TeamRoleAdmin() Role {
	return Role{Name: "admin", Permissions: append(
		TeamRoleEditor().Permissions, PermOrganizationAdmin,
	)}
}

// TeamRoleByName resolves a catalog role from its name. Unknown names
// (including custom per-invite roles) return false — callers decide
// whether to reject or pass the custom role through.
func TeamRoleByName(name string) (Role, bool) {
	switch name {
	case "viewer":
		return TeamRoleViewer(), true
	case "editor":
		return TeamRoleEditor(), true
	case "admin":
		return TeamRoleAdmin(), true
	}
	return Role{}, false
}
