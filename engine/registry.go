// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"fmt"
	"strings"
	"sync"

	"github.com/dazyflow/dazyflow/core"
)

// Registry holds the set of native modules available to the engine. It is
// safe for concurrent reads; registration is expected to happen during init.
type Registry struct {
	mu    sync.RWMutex
	nodes map[string]NativeDrop
	// derived caches DerivedManifests. Applying the universal transforms
	// costs an allocation per port slice per drop — half a megabyte across
	// the built-in catalog — and the catalog is asked for on every graph
	// validation, save and submit, which are request paths. Registration
	// happens at init, so this is built once and dropped only if a drop
	// registers later.
	derived map[string]core.Manifest
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
	// The runner/ namespace belongs to tenant runners. Refusing it here means a
	// future built-in can never shadow an org's own step — which would change
	// what an existing flow runs, silently, on upgrade.
	if strings.HasPrefix(n.Manifest.ID, RunnerNamespace) {
		return fmt.Errorf("drop %q: the %q prefix is reserved for tenant runners", n.Manifest.ID, RunnerNamespace)
	}
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
	r.derived = nil
	return nil
}

// DerivedManifests returns every registered manifest with the universal
// transforms applied — the passthrough pin and list-port marking that the
// palette, validation and input assembly all expect.
//
// The map and the manifests in it are SHARED with every other caller: read
// them, and clone the map before handing it to anything that edits its own
// view of the catalog.
func (r *Registry) DerivedManifests() map[string]core.Manifest {
	r.mu.RLock()
	derived := r.derived
	r.mu.RUnlock()
	if derived != nil {
		return derived
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.derived == nil {
		d := make(map[string]core.Manifest, len(r.nodes))
		for id, n := range r.nodes {
			d[id] = core.MarkListPorts(core.WithPassthrough(n.Manifest))
		}
		r.derived = d
	}
	return r.derived
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
