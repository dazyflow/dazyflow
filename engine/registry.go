package engine

import (
	"fmt"
	"sync"

	"git.sr.ht/~klahr/hazyflow/core"
)

// Registry holds the set of native modules available to the engine. It is
// safe for concurrent reads; registration is expected to happen during init.
type Registry struct {
	mu    sync.RWMutex
	nodes map[string]NativeDrop
}

func NewRegistry() *Registry {
	return &Registry{
		nodes: make(map[string]NativeDrop),
	}
}

// Register adds a native module. Returns an error if a module with the same
// ID is already registered — caller chooses whether to panic or recover.
//
// The Summary and Examples fields are required: a new integration must
// state in one sentence what it does and supply at least one worked
// params example. The catalog API surfaces both verbatim, so an LLM
// composing a flow has the metadata it needs to call the drop
// correctly. Fail-closed here keeps the contract honest — drops can't
// silently ship without the discovery shape the LLM relies on.
func (r *Registry) Register(n NativeDrop) error {
	if n.Manifest.ID == "" {
		return fmt.Errorf("manifest ID is empty")
	}
	if n.Execute == nil {
		return fmt.Errorf("module %q has no Execute function", n.Manifest.ID)
	}
	if n.Manifest.Summary == "" {
		return fmt.Errorf("module %q: Manifest.Summary is required (one-sentence LLM-friendly description)", n.Manifest.ID)
	}
	if len(n.Manifest.Examples) == 0 {
		return fmt.Errorf("module %q: Manifest.Examples must contain at least one ParamsExample", n.Manifest.ID)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.nodes[n.Manifest.ID]; exists {
		return fmt.Errorf("module %q already registered", n.Manifest.ID)
	}
	r.nodes[n.Manifest.ID] = n
	return nil
}

func (r *Registry) Get(id string) (core.Transport, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n, ok := r.nodes[id]
	if !ok {
		return nil, false
	}
	return &nativeTransport{node: n}, true
}

// Manifests returns a snapshot of all registered manifests, keyed by module
// ID — useful for graph validation.
func (r *Registry) Manifests() map[string]core.Manifest {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]core.Manifest, len(r.nodes))
	for id, n := range r.nodes {
		out[id] = n.Manifest
	}
	return out
}

// Default is the package-level registry that init()-based modules register
// into.
var Default = NewRegistry()

// Register is a convenience wrapper that registers into Default. Panics on
// error — registration mistakes should fail loud at startup, not silently
// produce a half-built engine.
func Register(n NativeDrop) {
	if err := Default.Register(n); err != nil {
		panic(err)
	}
}
