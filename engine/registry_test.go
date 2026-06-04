package engine

import (
	"context"
	"testing"

	"git.sr.ht/~klahr/hazyflow/core"
)

// drop builds a minimal valid NativeDrop (Register requires Summary +
// Examples) with the given ID and aliases.
func drop(id string, aliases ...string) NativeDrop {
	return NativeDrop{
		Manifest: core.Manifest{
			ID:       id,
			Summary:  "test drop",
			Aliases:  aliases,
			Examples: []core.ParamsExample{{Title: "x"}},
		},
		Execute: func(context.Context, core.Job, chan<- core.Progress) (core.Result, error) {
			return core.Result{Status: core.StatusOK}, nil
		},
	}
}

// A pre-rename module ID listed in Aliases resolves to the canonical drop for
// execution, and the alias does NOT shadow lookups of the canonical ID.
func TestRegistry_AliasResolvesForExecution(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(drop("delay", "sleep")); err != nil {
		t.Fatalf("register: %v", err)
	}

	for _, id := range []string{"delay", "sleep"} {
		tr, ok := r.Get(id)
		if !ok {
			t.Fatalf("Get(%q): not found", id)
		}
		if tr.Manifest().ID != "delay" {
			t.Errorf("Get(%q) resolved to %q, want canonical \"delay\"", id, tr.Manifest().ID)
		}
	}
	if _, ok := r.Get("nope"); ok {
		t.Error("Get(unknown) should miss")
	}
}

// Manifests() surfaces the alias as a Hidden copy (so validation of a
// pre-rename graph passes) while the canonical entry stays visible — the
// catalog filters Hidden, so the palette shows the drop once.
func TestRegistry_AliasManifestIsHidden(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(drop("delay", "sleep")); err != nil {
		t.Fatalf("register: %v", err)
	}
	mans := r.Manifests()

	canon, ok := mans["delay"]
	if !ok || canon.Hidden {
		t.Errorf("canonical \"delay\" missing or hidden: %+v", canon)
	}
	alias, ok := mans["sleep"]
	if !ok {
		t.Fatal("alias \"sleep\" missing from Manifests() — validation of old graphs would fail")
	}
	if !alias.Hidden {
		t.Error("alias \"sleep\" should be Hidden so the catalog drops it")
	}
	if alias.ID != "delay" {
		t.Errorf("alias manifest ID = %q, want canonical \"delay\"", alias.ID)
	}
}

// An alias that collides with a real module ID (or another alias) is rejected
// at registration rather than silently shadowing.
func TestRegistry_AliasCollisionRejected(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(drop("real")); err != nil {
		t.Fatalf("register real: %v", err)
	}
	if err := r.Register(drop("other", "real")); err == nil {
		t.Error("alias colliding with an existing module ID should be rejected")
	}

	r2 := NewRegistry()
	if err := r2.Register(drop("a", "shared")); err != nil {
		t.Fatalf("register a: %v", err)
	}
	if err := r2.Register(drop("b", "shared")); err == nil {
		t.Error("two drops claiming the same alias should be rejected")
	}
}
