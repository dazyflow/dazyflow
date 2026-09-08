// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"errors"
	"fmt"
	"time"
)

type Principal struct {
	Subject   string         `json:"sub"`
	Tenant    string         `json:"tenant"`
	Workspace string         `json:"workspace,omitempty"`
	Roles     []Role         `json:"roles,omitempty"`
	Extras    map[string]any `json:"extras,omitempty"`
}

func (p Principal) Permissions() map[Permission]struct{} {
	out := make(map[Permission]struct{})
	for _, r := range p.Roles {
		for _, perm := range r.Permissions {
			out[perm] = struct{}{}
		}
	}
	return out
}

func (p Principal) Has(perm Permission) bool {
	for _, r := range p.Roles {
		if r.Has(perm) {
			return true
		}
	}
	return false
}

var ErrUnauthorized = errors.New("unauthorized")

func Require(p Principal, perms ...Permission) error {
	for _, perm := range perms {
		if !p.Has(perm) {
			return fmt.Errorf("%w: missing %s", ErrUnauthorized, perm)
		}
	}
	return nil
}

func CanAdminOrg(p Principal) bool {
	return p.Has(PermOrganizationAdmin) || p.Has(PermPlatformAdmin)
}

// Never allows cross-tenant access, not even for another tenant's admin.
func RequireTenant(p Principal, tenant string) error {
	// Platform admins cross tenant boundaries by design, and often carry no tenant.
	if p.Has(PermPlatformAdmin) {
		return nil
	}
	if p.Tenant == "" {
		return fmt.Errorf("%w: principal has no tenant", ErrUnauthorized)
	}
	if tenant != "" && p.Tenant != tenant {
		return fmt.Errorf("%w: principal tenant %q cannot access tenant %q",
			ErrUnauthorized, p.Tenant, tenant)
	}
	return nil
}

func RequireWorkspace(p Principal, tenant, _ string) error {
	return RequireTenant(p, tenant)
}

func AuthorizeGraphRun(p Principal, graph Graph) error {
	if err := RequireWorkspace(p, graph.Tenant, graph.Workspace); err != nil {
		return err
	}
	if err := authorizeVisibility(p, graph); err != nil {
		return err
	}
	return Require(p, PermGraphRun)
}

// Callers translate ErrUnauthorized to 404 rather than 403, so a private flow
// does not leak its existence.
func AuthorizeGraphView(p Principal, graph Graph) error {
	if err := RequireWorkspace(p, graph.Tenant, graph.Workspace); err != nil {
		return err
	}
	return authorizeVisibility(p, graph)
}

func AuthorizeGraphEdit(p Principal, graph Graph) error {
	if err := RequireWorkspace(p, graph.Tenant, graph.Workspace); err != nil {
		return err
	}
	if err := Require(p, PermGraphEdit); err != nil {
		return err
	}
	if graph.Owner == "" {
		return nil
	}
	if isOwner(p, graph) || IsFlowAdminPrincipal(p) {
		return nil
	}
	return fmt.Errorf("%w: flow %q is owned by %q", ErrUnauthorized, graph.ID, graph.Owner)
}

// A CAPABILITY check, deliberately NOT routed through RequireTenant: a support
// agent's own tenant is irrelevant and often absent, so those would wrongly
// reject it. The (agent, tenant, flow) tuple in the grant is the sole authority.
//
// Read-only ONLY — this must never gate Run or Edit, and even with a valid grant
// the caller serves the REDACTED view.
func AuthorizeGraphSupportView(p Principal, graph Graph, grant AccessGrant, now time.Time) error {
	if !p.Has(PermSupportAgent) {
		return fmt.Errorf("%w: not a support agent", ErrUnauthorized)
	}
	if p.Subject == "" || grant.AgentSubject != p.Subject {
		return fmt.Errorf("%w: grant is not for this agent", ErrUnauthorized)
	}
	if grant.Tenant != graph.Tenant || grant.FlowID != graph.ID {
		return fmt.Errorf("%w: grant does not cover flow %q in tenant %q",
			ErrUnauthorized, graph.ID, graph.Tenant)
	}
	if !grant.IsActive(now) {
		return fmt.Errorf("%w: grant is not active (status %q / expired / revoked)",
			ErrUnauthorized, grant.Status)
	}
	return nil
}

func authorizeVisibility(p Principal, graph Graph) error {
	if graph.EffectiveVisibility() == VisibilityOrg {
		return nil
	}
	if graph.Owner == "" {
		return nil
	}
	if isOwner(p, graph) || IsFlowAdminPrincipal(p) {
		return nil
	}
	return fmt.Errorf("%w: flow %q is private", ErrUnauthorized, graph.ID)
}

func isOwner(p Principal, graph Graph) bool {
	return graph.Owner != "" && p.Subject != "" && p.Subject == graph.Owner
}

// Lets an administrator recover an otherwise-private flow when its owner leaves.
// Exported so the save path can gate owner reassignment on it.
func IsFlowAdminPrincipal(p Principal) bool {
	return p.Has(PermOrganizationAdmin) || p.Has(PermGraphAdmin)
}
