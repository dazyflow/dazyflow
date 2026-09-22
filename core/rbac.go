// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import "slices"

type Permission string

const (
	PermGraphRun          Permission = "graph:run"
	PermGraphEdit         Permission = "graph:edit"
	PermGraphAdmin        Permission = "graph:admin"
	PermModuleRegister    Permission = "module:register"
	PermSecretRead        Permission = "secret:read"
	PermSecretWrite       Permission = "secret:write"
	PermOrganizationAdmin Permission = "organization:admin"
	PermPlatformAdmin     Permission = "platform:admin"
	// DELIBERATELY WEAK: the support queue, a ticket chat, and requesting a grant.
	// It does NOT cross tenant and does NOT imply platform:admin — a flow is
	// reachable only through an approved AccessGrant, and even then as the REDACTED
	// view. Never add this to RequireTenant's short-circuit.
	PermSupportAgent Permission = "support:agent"
)

type Role struct {
	Name        string       `json:"name"`
	Permissions []Permission `json:"permissions"`
}

func PlatformAdminRole() Role {
	return Role{Name: "platform_admin", Permissions: []Permission{PermPlatformAdmin}}
}

// Holds ONLY PermSupportAgent, so an agent has no ambient access to any tenant's
// flows, secrets or runs.
func SupportAgentRole() Role {
	return Role{Name: "support_agent", Permissions: []Permission{PermSupportAgent}}
}

func (r Role) Has(p Permission) bool {
	return slices.Contains(r.Permissions, p)
}

// One source of truth, so signup, invites and role changes cannot drift apart —
// they used to. Constructors return fresh values, so a caller mutating its copy
// cannot poison the catalog.

func TeamRoleViewer() Role {
	return Role{Name: "viewer", Permissions: []Permission{PermGraphRun}}
}

// Deliberately NOT graph:admin: that bypasses per-flow privacy (IsFlowAdminPrincipal)
// and gates publishing, and editor is the default invite/signup role.
func TeamRoleEditor() Role {
	return Role{Name: "editor", Permissions: []Permission{
		PermGraphRun, PermGraphEdit,
		PermSecretRead, PermSecretWrite,
	}}
}

// Deliberately NOT platform:admin, the cross-tenant operator role, which is
// never part of this catalog.
func TeamRoleAdmin() Role {
	return Role{Name: "admin", Permissions: append(
		TeamRoleEditor().Permissions, PermGraphAdmin, PermOrganizationAdmin,
	)}
}

// legacyEditorPermissions is what TeamRoleEditor granted before graph:admin was
// removed from it. Roles are persisted as permission lists, so existing
// sessions, keys, users and memberships still carry it.
var legacyEditorPermissions = []Permission{
	PermGraphRun, PermGraphEdit, PermGraphAdmin,
	PermSecretRead, PermSecretWrite,
}

// UpgradeLegacyRoles replaces a stored "editor" role carrying exactly the old
// catalog permission set with the current catalog editor, so a persisted role
// cannot keep graph:admin. Custom roles (any other permission set) are kept
// verbatim, and so is everything for an org admin, who is entitled to
// graph:admin anyway (a legacy signup owner held it only via editor). Returns
// roles itself when nothing changes.
func UpgradeLegacyRoles(roles []Role) []Role {
	for _, r := range roles {
		if r.Has(PermOrganizationAdmin) {
			return roles
		}
	}
	var out []Role
	for i, r := range roles {
		if r.Name != "editor" || !samePermissionSet(r.Permissions, legacyEditorPermissions) {
			continue
		}
		if out == nil {
			out = slices.Clone(roles)
		}
		out[i] = TeamRoleEditor()
	}
	if out == nil {
		return roles
	}
	return out
}

func samePermissionSet(a, b []Permission) bool {
	for _, p := range a {
		if !slices.Contains(b, p) {
			return false
		}
	}
	for _, p := range b {
		if !slices.Contains(a, p) {
			return false
		}
	}
	return true
}

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
