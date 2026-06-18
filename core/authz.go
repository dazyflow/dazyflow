package core

import (
	"errors"
	"fmt"
)

// Principal represents an authenticated identity carried through the system.
// Users come from OIDC; service accounts come from API keys; both end up as a
// Principal once authenticated, so authorization logic doesn't need to care.
type Principal struct {
	Subject   string         `json:"sub"`
	Tenant    string         `json:"tenant"`
	Workspace string         `json:"workspace,omitempty"`
	Roles     []Role         `json:"roles,omitempty"`
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

// CanAdminOrg reports whether the principal may perform organization-admin
// actions on the org it's acting in. True for an organization admin, and
// also for a platform admin — the cross-tenant super-admin is a superset
// that can administer any org (RequireTenant already lets it cross tenant
// boundaries). Use this instead of a bare Has(PermOrganizationAdmin) so a
// platform operator isn't locked out of per-org settings.
func CanAdminOrg(p Principal) bool {
	return p.Has(PermOrganizationAdmin) || p.Has(PermPlatformAdmin)
}

// RequireTenant checks that the principal's tenant matches the requested
// one. Cross-tenant access is never allowed, even for tenant admins of a
// different tenant.
func RequireTenant(p Principal, tenant string) error {
	// Platform admins cross tenant boundaries by design — the role's
	// whole purpose is operating multiple tenants on one dzd. The
	// principal itself may not even carry a tenant (operator keys
	// often don't).
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

// RequireWorkspace ensures the principal is scoped to (or admin over) the
// given workspace within its tenant. Tenant admins implicitly pass.
func RequireWorkspace(p Principal, tenant, workspace string) error {
	if err := RequireTenant(p, tenant); err != nil {
		return err
	}
	if p.Has(PermOrganizationAdmin) {
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
	if err := authorizeVisibility(p, graph); err != nil {
		return err
	}
	return Require(p, PermGraphRun)
}

// AuthorizeGraphView returns nil if p may see this flow at all
// (load, list, run). Org-visible flows allow any tenant/workspace
// member; private flows require the principal to be the owner or
// carry organization:admin / graph:admin.
//
// Returns ErrUnauthorized when the principal lacks read access —
// callers typically translate that to a 404 (NOT 403) at the HTTP
// boundary so private flows don't leak their existence to others.
func AuthorizeGraphView(p Principal, graph Graph) error {
	if err := RequireWorkspace(p, graph.Tenant, graph.Workspace); err != nil {
		return err
	}
	return authorizeVisibility(p, graph)
}

// AuthorizeGraphEdit gates save/delete. An org-visible flow is still
// only writable by its owner (or admin) — sharing for read doesn't
// imply shared write. New flows (Owner=="") are writable by anyone
// with graph:edit in the workspace; that's the only path a flow gets
// its initial Owner stamped.
func AuthorizeGraphEdit(p Principal, graph Graph) error {
	if err := RequireWorkspace(p, graph.Tenant, graph.Workspace); err != nil {
		return err
	}
	if err := Require(p, PermGraphEdit); err != nil {
		return err
	}
	// New-flow case: no owner yet, fall through. The save path will
	// stamp the principal as owner.
	if graph.Owner == "" {
		return nil
	}
	if isOwner(p, graph) || IsFlowAdminPrincipal(p) {
		return nil
	}
	return fmt.Errorf("%w: flow %q is owned by %q", ErrUnauthorized, graph.ID, graph.Owner)
}

// authorizeVisibility is the shared read-side check used by View and
// (indirectly) Run.
func authorizeVisibility(p Principal, graph Graph) error {
	if graph.EffectiveVisibility() == VisibilityOrg {
		return nil
	}
	// Private. Owner unset means the flow predates visibility — treat
	// as org-visible so legacy flows keep working.
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

// IsFlowAdminPrincipal is the override that lets administrators
// recover otherwise-private flows — important when the original owner
// leaves the tenant. graph:admin is the per-graph admin; organization:admin
// subsumes it. Exported so the daemon's save path can use it to gate
// owner reassignment.
func IsFlowAdminPrincipal(p Principal) bool {
	return p.Has(PermOrganizationAdmin) || p.Has(PermGraphAdmin)
}
