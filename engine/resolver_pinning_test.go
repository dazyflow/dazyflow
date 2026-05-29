package engine

import (
	"context"
	"testing"

	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/engine/jsdrop"
)

// dropSrc builds a minimal valid scripted drop with a given id + version. The
// catalog no longer parses the manifest from source (the Node runtime does);
// the source only has to transpile, and addDrop passes the manifest explicitly.
func dropSrc(id, version string) string {
	return `export default { manifest: {
		id: "` + id + `", version: "` + version + `", label: "` + id + `",
		summary: "test drop.", outputs: [{ port: "out" }],
		examples: [{ title: "x", params: {} }],
	}, run() { return { out: "` + id + `@` + version + `" }; } };`
}

// scriptEcho stands in for the Node-host Transport so resolution/pinning can be
// asserted without a real Node runtime.
type scriptEcho struct{ m core.Manifest }

func (e scriptEcho) Manifest() core.Manifest { return e.m }
func (e scriptEcho) Execute(context.Context, core.Job, chan<- core.Progress) (core.Result, error) {
	return core.Result{Status: core.StatusOK}, nil
}

func addDrop(t *testing.T, c *jsdrop.Catalog, id, version string, global bool) {
	t.Helper()
	if c.Run == nil {
		c.Run = func(m core.Manifest, _ string, _ bool) core.Transport { return scriptEcho{m: m} }
	}
	man := core.Manifest{
		ID: id, Version: version, Label: id, Summary: "test drop.",
		Outputs:  []core.Port{{Port: "out"}},
		Examples: []core.ParamsExample{{Title: "x"}},
	}
	if _, _, err := c.AddPrebuilt(id+version, dropSrc(id, version), man, global, false); err != nil {
		t.Fatalf("AddPrebuilt %s@%s: %v", id, version, err)
	}
}

// The NodeResolver reads the tenant off the context and routes scripted
// resolution through the per-tenant catalog: a drop one tenant installed is
// invisible to another, and the global-default ("") set is not.
func TestNodeResolver_PerTenantIsolation(t *testing.T) {
	cat := jsdrop.NewCatalog()
	addDrop(t, cat, "stripe", "1.0.0", false) // not global
	if err := cat.Install("acme", "stripe", "1.0.0"); err != nil {
		t.Fatalf("install: %v", err)
	}
	r := &NodeResolver{Script: cat}

	if _, err := r.Resolve(core.WithTenant(context.Background(), "acme"), "stripe"); err != nil {
		t.Errorf("acme should resolve its own install: %v", err)
	}
	if _, err := r.Resolve(core.WithTenant(context.Background(), "globex"), "stripe"); err == nil {
		t.Error("globex resolved a drop only acme installed — tenant isolation broken")
	}
	if _, err := r.Resolve(context.Background(), "stripe"); err == nil {
		t.Error("global-default tenant resolved a tenant-private drop")
	}
}

// A node may pin "id@version" so an install-update (a newer version added to
// the tenant's set) can't silently swap the transport a running flow uses; a
// bare id tracks the latest installed version.
func TestNodeResolver_VersionPinning(t *testing.T) {
	cat := jsdrop.NewCatalog()
	addDrop(t, cat, "stripe", "1.0.0", false)
	addDrop(t, cat, "stripe", "2.0.0", false)
	for _, v := range []string{"1.0.0", "2.0.0"} {
		if err := cat.Install("acme", "stripe", v); err != nil {
			t.Fatalf("install %s: %v", v, err)
		}
	}
	r := &NodeResolver{Script: cat}
	ctx := core.WithTenant(context.Background(), "acme")

	pinned, err := r.Resolve(ctx, "stripe@1.0.0")
	if err != nil || pinned.Manifest().Version != "1.0.0" {
		t.Errorf("pinned resolve = (%v, %v), want version 1.0.0", versionOf(pinned), err)
	}
	latest, err := r.Resolve(ctx, "stripe")
	if err != nil || latest.Manifest().Version != "2.0.0" {
		t.Errorf("bare resolve = (%v, %v), want latest 2.0.0", versionOf(latest), err)
	}
}

// Native drops live in the bare-id world: a pin is ignored rather than failing
// resolution, so a built-in keeps resolving whatever a graph writes after "@".
func TestNodeResolver_NativeIgnoresPin(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Register(NativeDrop{
		Manifest: core.Manifest{
			ID:       "delay",
			Version:  "1.0.0",
			Summary:  "test native drop.",
			Examples: []core.ParamsExample{{Title: "x"}},
		},
		Execute: func(context.Context, core.Job, chan<- core.Progress) (core.Result, error) {
			return core.Result{Status: core.StatusOK}, nil
		},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	r := &NodeResolver{Native: reg}
	if _, err := r.Resolve(context.Background(), "delay@9.9.9"); err != nil {
		t.Errorf("native resolve with a pin should ignore the version: %v", err)
	}
}

func versionOf(t core.Transport) string {
	if t == nil {
		return "<nil>"
	}
	return t.Manifest().Version
}
