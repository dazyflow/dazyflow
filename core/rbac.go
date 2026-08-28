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
	// PermPlatformAdmin is the cross-tenant super-admin role. Carriers
	// can see and act on every tenant on the dzd instance — manage
	// keys, list runs, issue keys in any tenant, etc. Distinct from
	// organization:admin (which is per-tenant). For SaaS-style hosting
	// where the operator runs dzd for many customer orgs.
	PermPlatformAdmin Permission = "platform:admin"
	// PermSupportAgent is the DELIBERATELY WEAK support role. By itself it
	// grants only: read the support queue, post to a ticket chat, and request
	// an access grant. It does NOT cross tenant and does NOT imply
	// platform:admin — a support agent reaches a specific flow ONLY through an
	// approved, time-boxed AccessGrant (a capability), and even then sees only
	// the REDACTED support view. Never add this to RequireTenant's
	// short-circuit. See AuthorizeGraphSupportView and
	// docs/support-tickets-design.md.
	PermSupportAgent Permission = "support:agent"
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

// SupportAgentRole is the role a support agent carries. It holds ONLY
// PermSupportAgent — the weak, grant-gated support permission — so an agent has
// no ambient access to any tenant's flows, secrets, or runs; a specific flow is
// reachable only via an approved AccessGrant (see AuthorizeGraphSupportView).
// Who fills this role differs per deployment (hosted: vendor staff via an env
// allowlist; self-host: an org admin helping their own members).
func SupportAgentRole() Role {
	return Role{Name: "support_agent", Permissions: []Permission{PermSupportAgent}}
}

func (r Role) Has(p Permission) bool {
	return slices.Contains(r.Permissions, p)
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
