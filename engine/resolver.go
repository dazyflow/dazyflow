// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"
	"fmt"
	"maps"
	"strings"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/engine/mcp"
	"github.com/dazyflow/dazyflow/engine/webapi"
)

// Resolver finds the Transport for a module id, applying the priority order.
// ctx carries the tenant, so scripted catalogs return that tenant's installed
// drops. moduleID may pin an exact version as "id@version"; a bare id resolves
// the latest visible to the tenant.
type Resolver interface {
	Resolve(ctx context.Context, moduleID string) (core.Transport, error)
}

func splitModuleVersion(moduleID string) (id, version string) {
	if i := strings.LastIndex(moduleID, "@"); i > 0 {
		return moduleID[:i], moduleID[i+1:]
	}
	return moduleID, ""
}

type resolverCtxKey struct{}

func WithResolver(ctx context.Context, r Resolver) context.Context {
	if r == nil {
		return ctx
	}
	return context.WithValue(ctx, resolverCtxKey{}, r)
}

type NodeResolver struct {
	Native *Registry
	Remote *RemoteCatalog
	MCP    *mcp.Catalog
	WebAPI *webapi.Catalog

	// DropGate is the platform-admin killswitch, consulted after a transport is
	// found but before it is returned; a non-nil error means the node never executes.
	// A hook rather than a store dependency, so engine doesn't import the daemon's
	// persistence layer.
	DropGate func(ctx context.Context, dropID, tenant string) error
}

func (r *NodeResolver) Resolve(ctx context.Context, moduleID string) (core.Transport, error) {
	id, _ := splitModuleVersion(moduleID)

	t, ok := r.lookup(ctx, id)
	if !ok {
		return nil, fmt.Errorf("no transport registered for module %q", moduleID)
	}
	// Check the killswitch after lookup, so an unknown id still reports "no
	// transport" rather than a confusing "disabled".
	if r.DropGate != nil {
		tenant, _ := core.TenantFromContext(ctx)
		if err := r.DropGate(ctx, id, tenant); err != nil {
			return nil, err
		}
	}
	return t, nil
}

// lookup takes a ctx solely to read the executing tenant: native and MCP drops
// are instance-wide, but a remote belongs to exactly one tenant and must not be
// reachable from another. Passing the tenant one level below DropGate is what
// makes cross-tenant resolution impossible rather than merely forbidden.
func (r *NodeResolver) lookup(ctx context.Context, id string) (core.Transport, bool) {
	if r.Native != nil {
		if t, ok := r.Native.Get(id); ok {
			return t, true
		}
	}
	if r.Remote != nil {
		tenant, _ := core.TenantFromContext(ctx)
		if t, ok := r.Remote.Get(tenant, id); ok {
			return t, true
		}
	}
	if r.MCP != nil {
		// Same reason as Remote: an org's MCP server holds that org's credential, and a
		// job reaching a transport carries RESOLVED secrets. An absent tenant sees only
		// the operator's instance-wide servers.
		tenant, _ := core.TenantFromContext(ctx)
		if t, ok := r.MCP.Get(tenant, id); ok {
			return t, true
		}
	}
	if r.WebAPI != nil {
		tenant, _ := core.TenantFromContext(ctx)
		if t, ok := r.WebAPI.Get(tenant, id); ok {
			return t, true
		}
	}
	return nil, false
}

// ManifestsForTenant is what ValidateWithManifests sees before execution.
// Native drops are instance-wide; remotes and MCP servers have both an
// operator-configured population and a per-org one. Showing one tenant's runner
// drops to another would put an unresolvable step in the palette and, worse, tell
// them a runner by that name exists somewhere.
func (r *NodeResolver) ManifestsForTenant(tenant string) map[string]core.Manifest {
	out := map[string]core.Manifest{}
	if r.Native != nil {
		out = maps.Clone(r.Native.DerivedManifests())
		if out == nil {
			out = map[string]core.Manifest{}
		}
	}
	if r.Remote != nil {
		// An empty tenant yields nothing, matching Get: docs generation and the support
		// view want built-ins, not one org's private steps.
		//
		// keepExisting, because lookup prefers Native. Letting a remote overwrite a
		// native id would show its manifest in the palette and in validation while every
		// run executed the built-in — the catalog and the executor disagreeing, silently.
		// RemoteCatalog.Reserved refuses such a registration; this is the belt-and-braces
		// half, for a catalog wired without it.
		addKeeping(out, r.Remote.ManifestsFor(tenant))
	}
	if r.MCP != nil {
		addKeeping(out, r.MCP.ManifestsFor(tenant))
	}
	if r.WebAPI != nil {
		// keepExisting for the same reason, though an `api:`-prefixed id cannot collide
		// with a native one by construction.
		addKeeping(out, r.WebAPI.ManifestsFor(tenant))
	}
	return out
}

func (r *NodeResolver) ManifestsForSubset(tenant string, ids []string) map[string]core.Manifest {
	out := make(map[string]core.Manifest, len(ids))
	var derived map[string]core.Manifest
	if r.Native != nil {
		derived = r.Native.DerivedManifests()
	}
	var missing []string
	for _, id := range ids {
		if _, done := out[id]; done {
			continue
		}
		if m, ok := derived[id]; ok {
			out[id] = m
			continue
		}
		missing = append(missing, id)
	}
	if len(missing) == 0 {
		return out
	}
	// Resolve it exactly as the full-map path would, so precedence and tenant
	// scoping cannot drift.
	all := r.ManifestsForTenant(tenant)
	for _, id := range missing {
		if m, ok := all[id]; ok {
			out[id] = m
		}
	}
	return out
}

// addKeeping never overwrites an id dst already holds, so the map agrees with
// lookup's Native → Remote → MCP precedence.
func addKeeping(dst, src map[string]core.Manifest) {
	for id, m := range src {
		if _, taken := dst[id]; taken {
			continue
		}
		dst[id] = core.MarkListPorts(core.WithPassthrough(m))
	}
}

func (r *NodeResolver) Manifests() map[string]core.Manifest {
	return r.ManifestsForTenant("")
}

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
		// A platform admin cannot switch off a misbehaving tool that never appears on
		// the page the killswitch lives on.
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
