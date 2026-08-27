// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"
	"fmt"
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/engine/mcp"
)

// Resolver looks up the Transport responsible for executing a given module
// ID. The engine asks the resolver per node; the resolver implements the
// priority order (native → local descriptor → remote descriptor).
//
// ctx carries the tenant (core.WithTenant, set by the engine before Resolve)
// so scripted catalogs return that tenant's installed drops, isolated from
// other tenants. moduleID may pin an exact version as "id@version"; a bare id
// resolves the latest version visible to the tenant.
type Resolver interface {
	Resolve(ctx context.Context, moduleID string) (core.Transport, error)
}

// splitModuleVersion parses a module reference into its id and an optional
// pinned version. "gmail_send_email@2.0.0" → ("gmail_send_email", "2.0.0");
// a bare "gmail_send_email" → ("gmail_send_email", ""). The split is on the
// last "@" so a scoped-looking id is preserved, and the version drives exact
// resolution for scripted drops while the bare id feeds the version-blind
// native/local/remote/MCP catalogs.
func splitModuleVersion(moduleID string) (id, version string) {
	if i := strings.LastIndex(moduleID, "@"); i > 0 {
		return moduleID[:i], moduleID[i+1:]
	}
	return moduleID, ""
}

type resolverCtxKey struct{}

// WithResolver carries the engine's Resolver to an executing node so composite
// drops (for_each, subgraph) resolve their sub-modules through the SAME catalogs
// as top-level nodes — native, remote, MCP — not just the native registry. The engine wires this around every node's Execute.
func WithResolver(ctx context.Context, r Resolver) context.Context {
	if r == nil {
		return ctx
	}
	return context.WithValue(ctx, resolverCtxKey{}, r)
}

// NodeResolver is the default Resolver. It consults catalogs in order:
// native registry → remote (gRPC) modules → MCP tools. (A subprocess
// "local descriptor" catalog existed in the plugin era; it was never
// wired into dzd and was removed once every drop went native.)
type NodeResolver struct {
	Native *Registry
	Remote *RemoteCatalog
	MCP    *mcp.Catalog

	// DropGate, when set, is the platform-admin killswitch. It's consulted
	// after a transport is found but before it's returned, receiving the
	// resolved drop id and the executing tenant (from core.TenantFromContext,
	// set by the engine before Resolve). A non-nil error refuses the drop —
	// the node fails to resolve and never executes. A nil Gate (the default)
	// means no gating. Kept as a hook rather than a store dependency so the
	// engine package doesn't import the daemon's persistence layer.
	DropGate func(ctx context.Context, dropID, tenant string) error
}

func (r *NodeResolver) Resolve(ctx context.Context, moduleID string) (core.Transport, error) {
	id, _ := splitModuleVersion(moduleID)

	// Native / local / remote / MCP drops are version-blind: they live in the
	// bare-id world, so resolve them by id and ignore any pin (a built-in's
	// behavior doesn't fork per version).
	t, ok := r.lookup(ctx, id)
	if !ok {
		return nil, fmt.Errorf("no transport registered for module %q", moduleID)
	}
	// Killswitch: a platform admin may have disabled this drop globally or
	// for the executing tenant. Check after lookup so an unknown id still
	// reports "no transport" rather than a confusing "disabled".
	if r.DropGate != nil {
		tenant, _ := core.TenantFromContext(ctx)
		if err := r.DropGate(ctx, id, tenant); err != nil {
			return nil, err
		}
	}
	return t, nil
}

// lookup returns the first transport for id across the catalogs, in
// priority order (native → remote → MCP).
//
// Takes a ctx solely to read the executing tenant: native and MCP drops are
// instance-wide, but a remote belongs to exactly one tenant and must not be
// reachable from another. Resolve already had the tenant for DropGate; passing
// it one level further down is what makes cross-tenant resolution impossible
// rather than merely forbidden.
func (r *NodeResolver) lookup(ctx context.Context, id string) (core.Transport, bool) {
	if r.Native != nil {
		if t, ok := r.Native.Get(id); ok {
			return t, true
		}
	}
	if r.Remote != nil {
		// An absent tenant yields "", which Get treats as matching nothing.
		tenant, _ := core.TenantFromContext(ctx)
		if t, ok := r.Remote.Get(tenant, id); ok {
			return t, true
		}
	}
	if r.MCP != nil {
		// Same tenant argument, same reason as Remote: an org's own MCP server
		// holds that org's credential, and a job reaching a transport carries
		// RESOLVED secrets. An absent tenant sees only the operator's
		// instance-wide servers.
		tenant, _ := core.TenantFromContext(ctx)
		if t, ok := r.MCP.Get(tenant, id); ok {
			return t, true
		}
	}
	return nil, false
}

// ManifestsForTenant gathers every manifest visible to the tenant, for
// ValidateWithManifests before execution.
//
// Native drops are instance-wide. Remotes and MCP servers are not — or rather,
// not entirely: each has an operator-configured instance-wide population and a
// per-org one. A tenant runner's drops belong to one tenant, and showing them
// to another would put a step in the palette that tenant cannot resolve — and,
// worse, would tell them a runner by that name exists somewhere. The same holds
// for an org's own MCP server.
func (r *NodeResolver) ManifestsForTenant(tenant string) map[string]core.Manifest {
	out := map[string]core.Manifest{}
	add := func(src map[string]core.Manifest) {
		for id, m := range src {
			// Every processing drop carries the universal passthrough pin,
			// surfaced here once so the palette, validation, and input
			// assembly all see it without per-drop wiring. MarkListPorts then
			// tags list-carrying ports (rows/responses/…) so the editor can
			// flag a list wired into a one-at-a-time step.
			out[id] = core.MarkListPorts(core.WithPassthrough(m))
		}
	}
	if r.Native != nil {
		add(r.Native.Manifests())
	}
	if r.Remote != nil {
		// An empty tenant yields nothing, matching Get: the tenant-less callers
		// (docs generation, the support view) want built-ins, not one org's
		// private steps.
		//
		// keepExisting, because lookup() prefers Native. Letting a remote
		// overwrite a native id here would show its manifest in the palette
		// and in validation while every run executed the built-in — the
		// catalog and the executor disagreeing, silently. RemoteCatalog.
		// Reserved refuses such a registration outright; this is the
		// belt-and-braces half, for a catalog wired without it.
		addKeeping(out, r.Remote.ManifestsFor(tenant))
	}
	if r.MCP != nil {
		// Instance-wide servers plus this org's own — the same two populations
		// Get resolves across, so the palette shows exactly what will resolve.
		addKeeping(out, r.MCP.ManifestsFor(tenant))
	}
	return out
}

// addKeeping merges src into dst without overwriting an id dst already holds,
// so the map agrees with lookup()'s Native → Remote → MCP precedence.
func addKeeping(dst, src map[string]core.Manifest) {
	for id, m := range src {
		if _, taken := dst[id]; taken {
			continue
		}
		dst[id] = core.MarkListPorts(core.WithPassthrough(m))
	}
}

// Manifests gathers the instance-wide manifests — native and MCP, with no
// tenant's runners. For callers that have no tenant to scope by (docs
// generation, a unit harness); anything serving a request should use
// ManifestsForTenant so a runner reaches exactly the org that registered it.
func (r *NodeResolver) Manifests() map[string]core.Manifest {
	return r.ManifestsForTenant("")
}

// AllManifests gathers every drop on the instance INCLUDING every tenant's
// runner drops, with the tenants each remote id belongs to.
//
// The one legitimate caller is the platform killswitch page, which is
// instance-wide by definition: a platform admin has to be able to switch off a
// misbehaving tenant-runner drop, and ManifestsForTenant cannot show it to them
// without asking which org to look in. Nothing that ROUTES may use this — an id
// can belong to several tenants and this flattens them, which is exactly the
// confusion remoteKey exists to prevent.
func (r *NodeResolver) AllManifests() (map[string]core.Manifest, map[string][]string) {
	out := r.ManifestsForTenant("")
	var tenants map[string][]string
	if r.Remote != nil {
		remotes, remoteTenants := r.Remote.AllManifests()
		for id, m := range remotes {
			out[id] = core.MarkListPorts(core.WithPassthrough(m))
		}
		tenants = remoteTenants
	}
	if r.MCP != nil {
		// Tenant MCP servers need the same treatment as tenant runners: a
		// platform admin cannot switch off a misbehaving tool that never
		// appears on the page the killswitch lives on.
		mcps, mcpTenants := r.MCP.AllManifests()
		for id, m := range mcps {
			out[id] = core.MarkListPorts(core.WithPassthrough(m))
		}
		if tenants == nil {
			tenants = map[string][]string{}
		}
		for id, ts := range mcpTenants {
			tenants[id] = append(tenants[id], ts...)
		}
	}
	return out, tenants
}
