package core

import (
	"errors"
	"fmt"
)

// Principal represents an authenticated identity carried through the system.
// Users come from OIDC; service accounts come from API keys; both end up as a
// Principal once authenticated, so authorization logic doesn't need to care.
type Principal struct {
	Subject   string       `json:"sub"`
	Tenant    string       `json:"tenant"`
	Workspace string       `json:"workspace,omitempty"`
	Roles     []Role       `json:"roles,omitempty"`
	Extras    map[string]any `json:"extras,omitempty"`
}

// Permissions flattens every role's permission list into a single set for
// quick lookup.
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

// ErrUnauthorized is returned by enforcement helpers when the principal
// lacks a required permission or addresses a tenant/workspace they don't
// belong to.
var ErrUnauthorized = errors.New("unauthorized")

// Require checks that the principal holds every permission listed. The
// returned error wraps ErrUnauthorized so callers can use errors.Is.
func Require(p Principal, perms ...Permission) error {
	for _, perm := range perms {
		if !p.Has(perm) {
			return fmt.Errorf("%w: missing %s", ErrUnauthorized, perm)
		}
	}
	return nil
}

// RequireTenant checks that the principal's tenant matches the requested
// one. Cross-tenant access is never allowed, even for tenant admins of a
// different tenant.
func RequireTenant(p Principal, tenant string) error {
	if p.Tenant == "" {
		return fmt.Errorf("%w: principal has no tenant", ErrUnauthorized)
	}
	if tenant != "" && p.Tenant != tenant {
		return fmt.Errorf("%w: principal tenant %q cannot access tenant %q",
			ErrUnauthorized, p.Tenant, tenant)
	}
	return nil
}

// RequireWorkspace ensures the principal is scoped to (or admin over) the
// given workspace within its tenant. Tenant admins implicitly pass.
func RequireWorkspace(p Principal, tenant, workspace string) error {
	if err := RequireTenant(p, tenant); err != nil {
		return err
	}
	if p.Has(PermTenantAdmin) {
		return nil
	}
	if workspace != "" && p.Workspace != "" && p.Workspace != workspace {
		return fmt.Errorf("%w: principal workspace %q cannot access workspace %q",
			ErrUnauthorized, p.Workspace, workspace)
	}
	return nil
}

// AuthorizeGraphRun bundles the checks the engine should run before
// executing a graph on behalf of a principal.
func AuthorizeGraphRun(p Principal, graph Graph) error {
	if err := RequireWorkspace(p, graph.Tenant, graph.Workspace); err != nil {
		return err
	}
	return Require(p, PermGraphRun)
}
