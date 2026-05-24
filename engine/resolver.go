package engine

import (
	"fmt"

	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/engine/mcp"
)

// Resolver looks up the Transport responsible for executing a given module
// ID. The engine asks the resolver per node; the resolver implements the
// priority order (native → local descriptor → remote descriptor).
type Resolver interface {
	Resolve(moduleID string) (core.Transport, error)
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

func (r *NodeResolver) Resolve(moduleID string) (core.Transport, error) {
	if r.Native != nil {
		if t, ok := r.Native.Get(moduleID); ok {
			return t, nil
		}
	}
	if r.Local != nil {
		if t, ok := r.Local.Get(moduleID); ok {
			return t, nil
		}
	}
	if r.Remote != nil {
		if t, ok := r.Remote.Get(moduleID); ok {
			return t, nil
		}
	}
	if r.MCP != nil {
		if t, ok := r.MCP.Get(moduleID); ok {
			return t, nil
		}
	}
	return nil, fmt.Errorf("no transport registered for module %q", moduleID)
}

// Manifests gathers all manifests the resolver knows about. The engine uses
// this for ValidateWithManifests before execution.
func (r *NodeResolver) Manifests() map[string]core.Manifest {
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
