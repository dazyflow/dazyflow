package engine

import (
	"context"
	"testing"

	"git.sr.ht/~klahr/hazyflow/core"
)

// drop builds a minimal valid NativeDrop (Register requires Summary +
// Examples) with the given ID.
func drop(id string) NativeDrop {
	return NativeDrop{
		Manifest: core.Manifest{
			ID:       id,
			Summary:  "test drop",
			Examples: []core.ParamsExample{{Title: "x"}},
		},
		Execute: func(context.Context, core.Job, chan<- core.Progress) (core.Result, error) {
			return core.Result{Status: core.StatusOK}, nil
		},
	}
}

// A registered drop resolves by its ID; unknown IDs miss.
func TestRegistry_RegisterAndGet(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(drop("delay")); err != nil {
		t.Fatalf("register: %v", err)
	}

	tr, ok := r.Get("delay")
	if !ok {
		t.Fatal("Get(\"delay\"): not found")
	}
	if tr.Manifest().ID != "delay" {
		t.Errorf("Get(\"delay\") resolved to %q, want \"delay\"", tr.Manifest().ID)
	}
	if _, ok := r.Get("nope"); ok {
		t.Error("Get(unknown) should miss")
	}
	if _, ok := r.Manifests()["delay"]; !ok {
		t.Error("Manifests() should include \"delay\"")
	}
}

// Registering the same ID twice is rejected rather than silently shadowing.
func TestRegistry_DuplicateIDRejected(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(drop("real")); err != nil {
		t.Fatalf("register real: %v", err)
	}
	if err := r.Register(drop("real")); err == nil {
		t.Error("re-registering an existing module ID should be rejected")
	}
}
