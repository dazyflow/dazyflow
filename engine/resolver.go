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
	t, ok := r.lookup(id)
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
func (r *NodeResolver) lookup(id string) (core.Transport, bool) {
	if r.Native != nil {
		if t, ok := r.Native.Get(id); ok {
			return t, true
		}
	}
	if r.Remote != nil {
		if t, ok := r.Remote.Get(id); ok {
			return t, true
		}
	}
	if r.MCP != nil {
		if t, ok := r.MCP.Get(id); ok {
			return t, true
		}
	}
	return nil, false
}

// ManifestsForTenant gathers every manifest visible to the tenant, for
// ValidateWithManifests before execution.
func (r *NodeResolver) ManifestsForTenant(_ string) map[string]core.Manifest {
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
		add(r.Remote.Manifests())
	}
	if r.MCP != nil {
		add(r.MCP.Manifests())
	}
	return out
}

// Manifests gathers the global-default manifests. Back-compat shim for callers
// that aren't tenant-scoped; the engine prefers ManifestsForTenant.
func (r *NodeResolver) Manifests() map[string]core.Manifest {
	return r.ManifestsForTenant("")
}
