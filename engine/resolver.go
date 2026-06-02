package engine

import (
	"context"
	"fmt"
	"strings"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/engine/mcp"
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
// as top-level nodes — native, scripted, remote, MCP — not just the native
// registry. The engine wires this around every node's Execute.
func WithResolver(ctx context.Context, r Resolver) context.Context {
	if r == nil {
		return ctx
	}
	return context.WithValue(ctx, resolverCtxKey{}, r)
}

// ResolverFromContext returns the Resolver set by WithResolver, if any.
func ResolverFromContext(ctx context.Context) (Resolver, bool) {
	r, ok := ctx.Value(resolverCtxKey{}).(Resolver)
	return r, ok
}

// NodeResolver is the default Resolver. It consults catalogs in the order
// listed in the spec: native registry → local descriptors → remote
// descriptors → MCP tools.
type NodeResolver struct {
	Native *Registry
	Local  *LocalCatalog
	Remote *RemoteCatalog
	MCP    *mcp.Catalog
}

func (r *NodeResolver) Resolve(_ context.Context, moduleID string) (core.Transport, error) {
	id, _ := splitModuleVersion(moduleID)

	// Native / local / remote / MCP drops are version-blind: they live in the
	// bare-id world, so resolve them by id and ignore any pin (a built-in's
	// behavior doesn't fork per version).
	if r.Native != nil {
		if t, ok := r.Native.Get(id); ok {
			return t, nil
		}
	}
	if r.Local != nil {
		if t, ok := r.Local.Get(id); ok {
			return t, nil
		}
	}
	if r.Remote != nil {
		if t, ok := r.Remote.Get(id); ok {
			return t, nil
		}
	}
	if r.MCP != nil {
		if t, ok := r.MCP.Get(id); ok {
			return t, nil
		}
	}
	return nil, fmt.Errorf("no transport registered for module %q", moduleID)
}

// ManifestsForTenant gathers every manifest visible to the tenant, for
// ValidateWithManifests before execution.
func (r *NodeResolver) ManifestsForTenant(_ string) map[string]core.Manifest {
	out := map[string]core.Manifest{}
	if r.Native != nil {
		for id, m := range r.Native.Manifests() {
			out[id] = m
		}
	}
	if r.Local != nil {
		for id, m := range r.Local.Manifests() {
			out[id] = m
		}
	}
	if r.Remote != nil {
		for id, m := range r.Remote.Manifests() {
			out[id] = m
		}
	}
	if r.MCP != nil {
		for id, m := range r.MCP.Manifests() {
			out[id] = m
		}
	}
	return out
}

// Manifests gathers the global-default manifests. Back-compat shim for callers
// that aren't tenant-scoped; the engine prefers ManifestsForTenant.
func (r *NodeResolver) Manifests() map[string]core.Manifest {
	return r.ManifestsForTenant("")
}
