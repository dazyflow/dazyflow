package jsdrop

import (
	"context"
	"strings"
	"testing"

	"git.sr.ht/~klahr/hazy-flow/core"
)

// sentinelTransport is a stand-in returned by the Run hook so the test can tell
// hook-built (out-of-process Node) resolution apart from the in-process *Transport.
type sentinelTransport struct{ m core.Manifest }

func (s sentinelTransport) Manifest() core.Manifest { return s.m }
func (s sentinelTransport) Execute(context.Context, core.Job, chan<- core.Progress) (core.Result, error) {
	return core.Result{Status: core.StatusOK}, nil
}

// With the Run hook set, EVERY drop (official or runtime-installed) resolves
// through it, handed the transpiled ESM module. There is no in-process engine,
// so a catalog with no Run hook cannot execute drops at all.
func TestCatalog_RunRouting(t *testing.T) {
	c := NewCatalog()
	var gotESM string
	c.Run = func(m core.Manifest, jsESM string, _ bool) core.Transport {
		gotESM = jsESM
		return sentinelTransport{m: m}
	}

	mustAdd(t, c, "widget", "1.0.0", true)

	tr, ok := c.GetForTenant("", "widget", "1.0.0")
	if !ok {
		t.Fatal("drop did not resolve")
	}
	if _, isSentinel := tr.(sentinelTransport); !isSentinel {
		t.Errorf("drop should resolve via the Run hook, got %T", tr)
	}
	if gotESM == "" || !strings.Contains(gotESM, "export") {
		t.Errorf("Run hook should be handed the transpiled ESM module, got %q", gotESM)
	}
}

// A catalog with no Run hook can hold and list drops but cannot resolve one for
// execution — there is no in-process fallback engine.
func TestCatalog_NoRunHookDoesNotResolve(t *testing.T) {
	c := NewCatalog() // no Run hook
	if _, _, err := c.AddPrebuilt("widget1.0.0", dropSrc("widget", "1.0.0"), manifestFor("widget", "1.0.0"), true, false); err != nil {
		t.Fatalf("AddPrebuilt: %v", err)
	}
	// It's in the global palette…
	if _, ok := c.ManifestsForTenant("")["widget"]; !ok {
		t.Fatal("drop missing from palette")
	}
	// …but cannot be resolved for execution without the Node runtime hook.
	if _, ok := c.GetForTenant("", "widget", "1.0.0"); ok {
		t.Error("a drop must not resolve to a Transport without a Run hook")
	}
}
