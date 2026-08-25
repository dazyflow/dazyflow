// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"strings"
	"testing"

	nodepb "git.sr.ht/~klahr/dazyflow/api/gen/node"
	"git.sr.ht/~klahr/dazyflow/core"
)

// The three guard rails around tenant runners: the reserved namespace, what a
// tenant can see in its palette, and the refusal to send a file path to a
// machine that cannot read it.

// ---- the reserved namespace -------------------------------------------

// The runner/ prefix is reserved, and the native registry is what reserves it.
// Nothing produces such ids today, so this test is the only thing keeping the
// prefix from being quietly claimed by a built-in before anyone decides whether
// a namespaced remote scheme should come back.
func TestRegistry_RefusesTheRunnerNamespace(t *testing.T) {
	reg := NewRegistry()
	err := reg.Register(NativeDrop{
		Manifest: core.Manifest{
			ID:       RunnerNamespace + "box/fetch",
			Summary:  "a built-in trying to squat a runner id",
			Examples: []core.ParamsExample{{Title: "default"}},
		},
		Execute: noopExecute,
	})
	if err == nil {
		t.Fatal("a native drop was allowed to claim the runner/ namespace")
	}
	if !strings.Contains(err.Error(), "reserved") {
		t.Errorf("err = %v, want one saying the prefix is reserved", err)
	}
}

// The namespace is only reserved as a PREFIX — a drop merely containing the
// word must still be registrable, or the reservation would quietly outlaw
// ordinary names.
func TestRegistry_AllowsTheWordRunnerElsewhere(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Register(NativeDrop{
		Manifest: core.Manifest{
			ID:       "start_runner",
			Summary:  "an ordinary drop whose name contains the word",
			Examples: []core.ParamsExample{{Title: "default"}},
		},
		Execute: noopExecute,
	}); err != nil {
		t.Fatalf("a drop named start_runner was refused: %v", err)
	}
}

func TestManifestsForTenant_ShowsOnlyThatTenantsRunners(t *testing.T) {
	c := NewRemoteCatalog()
	defer c.Close()
	fakeRemote(c, "acme", "fetch")
	fakeRemote(c, "globex", "settle")

	reg := NewRegistry()
	if err := reg.Register(NativeDrop{
		Manifest: core.Manifest{
			ID:       "native_drop",
			Summary:  "an instance-wide built-in",
			Examples: []core.ParamsExample{{Title: "default"}},
		},
		Execute: noopExecute,
	}); err != nil {
		t.Fatalf("register native: %v", err)
	}
	r := &NodeResolver{Native: reg, Remote: c}

	acme := r.ManifestsForTenant("acme")
	if _, ok := acme["fetch"]; !ok {
		t.Error("acme cannot see its own runner's drop")
	}
	if _, ok := acme["settle"]; ok {
		t.Error("acme can see globex's runner drop")
	}
	// The positive control: without it, a ManifestsForTenant that returned
	// nothing at all would pass every assertion above.
	if _, ok := acme["native_drop"]; !ok {
		t.Error("built-ins vanished from the tenant palette")
	}
}

// The unscoped Manifests() is for callers with no tenant — docs generation, a
// unit harness. It must carry built-ins and NO tenant's private steps.
func TestManifests_UnscopedCarriesNoRunners(t *testing.T) {
	c := NewRemoteCatalog()
	defer c.Close()
	fakeRemote(c, "acme", "fetch")
	reg := NewRegistry()
	if err := reg.Register(NativeDrop{
		Manifest: core.Manifest{
			ID:       "native_drop",
			Summary:  "an instance-wide built-in",
			Examples: []core.ParamsExample{{Title: "default"}},
		},
		Execute: noopExecute,
	}); err != nil {
		t.Fatalf("register native: %v", err)
	}
	r := &NodeResolver{Native: reg, Remote: c}

	all := r.Manifests()
	if _, ok := all["native_drop"]; !ok {
		t.Error("the unscoped map lost the built-ins it exists to carry")
	}
	if _, ok := all["fetch"]; ok {
		t.Error("the unscoped map leaked a tenant's runner drop")
	}
}

// ---- the inline-only bound --------------------------------------------

// Ref.Ref is a path on the DAEMON's disk. Sending it to a step that cannot read
// it would fail inside the org's own code, reporting a missing file the org
// would reasonably read as their bug. Refuse before the step runs, naming the
// real cause.
//
// Driven by the MANIFEST, not by the transport, which is what makes it cover
// `run_on_runner` — a NATIVE drop that declares InlineOnly and used to receive
// a file ref, silently run its script with empty stdin, and report SUCCESS.
func TestRefuseInlineOnlyFileRefs(t *testing.T) {
	m := core.Manifest{
		ID: "fetch",
		Inputs: []core.Port{
			{Port: "in", InlineOnly: true},
			{Port: "notes"},
		},
	}
	err := refuseInlineOnlyFileRefs(m, map[string]core.Ref{
		"in": {Ref: "invoices/march.csv", MIME: "text/csv"},
	})
	if err == nil {
		t.Fatal("a file path reached a port that cannot read one")
	}
	// The message has to name the cause, the port, and the fix — this is the
	// error a flow author reads when their file-wired step stops working.
	for _, want := range []string{"in", "invoices/march.csv", "value"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, missing %q", err.Error(), want)
		}
	}
	// An inline value on the same port is the supported shape.
	if err := refuseInlineOnlyFileRefs(m, map[string]core.Ref{
		"in": {Inline: "march", MIME: "text/plain"},
	}); err != nil {
		t.Errorf("an inline value was refused: %v", err)
	}
	// A port that never declared the bound keeps taking files. Refusing every
	// file ref on every remote transport regressed co-located gRPC modules,
	// which read the daemon's disk perfectly well.
	if err := refuseInlineOnlyFileRefs(m, map[string]core.Ref{
		"notes": {Ref: "notes.txt"},
	}); err != nil {
		t.Errorf("a file was refused on a port that never declared InlineOnly: %v", err)
	}
}

// An inline value is the supported shape and must reach the runner.
//
// A real runner rather than a stub: the point is that the guard lets the
// supported case through to a genuine Execute, and a guard that refused
// everything would satisfy the refusal test above on its own.
func TestExecute_AllowsInlineValues(t *testing.T) {
	impl := &multiDrop{manifests: drops("fetch")}
	c := NewRemoteCatalog()
	defer c.Close()
	if err := register(t, c, "acme", "box", impl); err != nil {
		t.Fatalf("Register: %v", err)
	}
	tr, ok := c.Get("acme", "fetch")
	if !ok {
		t.Fatal("fetch not registered")
	}
	if _, err := tr.Execute(t.Context(), core.Job{
		ID:    "j1",
		Input: map[string]core.Ref{"in": {Inline: "march", MIME: "text/plain"}},
	}, nil); err != nil {
		t.Fatalf("an inline value was refused: %v", err)
	}
	if impl.lastDropID != "fetch" {
		t.Errorf("the job did not reach the runner (drop_id=%q)", impl.lastDropID)
	}
}

// The bound is declared on the manifest so the editor can say so on the port,
// rather than leaving a flow author to discover it from a failed run.
func TestRunnerManifest_MarksInputsInlineOnly(t *testing.T) {
	m := inlineOnlyInputs(core.Manifest{
		ID: "fetch",
		Inputs: []core.Port{
			{Port: "in"},
			{Port: "extra"},
		},
		Outputs: []core.Port{{Port: "out"}},
	})
	for _, p := range m.Inputs {
		if !p.InlineOnly {
			t.Errorf("input %q not marked inline-only", p.Port)
		}
	}
	// Outputs are unaffected: a runner may well return a value the daemon then
	// writes to a file itself.
	for _, p := range m.Outputs {
		if p.InlineOnly {
			t.Errorf("output %q was marked inline-only", p.Port)
		}
	}
}

// ---- presentation, and the one category a runner may not claim ----------

// A runner declares its own icon, category, description and tags. Without
// them its step lands in the palette as a generic box that search cannot find,
// so they are part of the wire contract rather than a later addition.
func TestManifestFromPB_CarriesPresentation(t *testing.T) {
	m := manifestFromPB(&nodepb.Manifest{
		Id:          "fetch",
		Label:       "Fetch invoices",
		Icon:        "receipt",
		Category:    "network",
		Subtitle:    "from the ledger",
		Description: "Pulls invoices issued since a date.",
		Summary:     "Fetch invoices from the ledger.",
		Tags:        []string{"invoice", "billing"},
	})
	if m.Icon != "receipt" || m.Category != "network" {
		t.Errorf("icon/category = %q/%q", m.Icon, m.Category)
	}
	if m.Subtitle == "" || m.Description == "" || m.Summary == "" {
		t.Error("a text field was dropped in translation")
	}
	if len(m.Tags) != 2 {
		t.Errorf("tags = %v, want both", m.Tags)
	}
}

// A trigger is a graph ENTRY POINT — the scheduler polls it, the webhook
// router dispatches to it. None of that machinery reaches a remote process, so
// a runner claiming the category would produce a flow that looks startable and
// never fires. Nothing would error, which is what makes it worth refusing.
func TestManifestFromPB_RefusesTheTriggerCategory(t *testing.T) {
	m := manifestFromPB(&nodepb.Manifest{Id: "fake_trigger", Category: "trigger"})
	if m.Category != "" {
		t.Fatalf("category = %q, want it dropped — a runner cannot be a trigger", m.Category)
	}
}

// An unknown category is a typo in a cosmetic field. Dropping it leaves the
// step ungrouped, which is visible and harmless; failing the registration
// would take a working runner offline over a spelling mistake.
func TestManifestFromPB_DropsAnUnknownCategory(t *testing.T) {
	m := manifestFromPB(&nodepb.Manifest{Id: "x", Category: "netwrok"})
	if m.Category != "" {
		t.Errorf("category = %q, want it dropped", m.Category)
	}
	// A control: the categories a runner MAY hold still come through, so this
	// is not just dropping everything.
	for c := range RunnerCategories {
		if got := manifestFromPB(&nodepb.Manifest{Id: "x", Category: c}).Category; got != c {
			t.Errorf("category %q was dropped", c)
		}
	}
}

// A remote may not take a built-in's id.
//
// lookup() prefers Native, but the manifest map used to add Remote after it —
// so the palette and validation showed the REMOTE's manifest while every run
// executed the built-in. Nothing errored; the flow author was reading a
// description of something that never ran.
func TestRegister_RefusesADropIdABuiltInAlreadyOwns(t *testing.T) {
	c := NewRemoteCatalog()
	defer c.Close()
	c.Reserved = func(id string) bool { return id == "http_request" }

	err := register(t, c, "acme", "box", &multiDrop{manifests: drops("http_request")})
	if err == nil {
		t.Fatal("a remote was allowed to shadow a built-in step")
	}
	for _, want := range []string{"http_request", "built-in"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, missing %q", err.Error(), want)
		}
	}
	// Refused whole: nothing half-registered.
	if _, ok := c.Get("acme", "http_request"); ok {
		t.Error("the refused drop was filed anyway")
	}
}

// The belt-and-braces half, for a catalog wired without Reserved: the manifest
// map must agree with lookup()'s Native → Remote → MCP precedence rather than
// letting the last writer win.
func TestManifestsForTenant_ARemoteDoesNotOverwriteANativeID(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Register(NativeDrop{
		Manifest: core.Manifest{
			ID:       "fetch",
			Summary:  "the built-in",
			Examples: []core.ParamsExample{{Title: "default"}},
		},
		Execute: noopExecute,
	}); err != nil {
		t.Fatalf("register native: %v", err)
	}
	c := NewRemoteCatalog()
	defer c.Close()
	if err := register(t, c, "acme", "box", &multiDrop{manifests: drops("fetch")}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	r := &NodeResolver{Native: reg, Remote: c}

	m := r.ManifestsForTenant("acme")["fetch"]
	if m.Summary != "the built-in" {
		t.Errorf("summary = %q, want the manifest of the drop that actually runs", m.Summary)
	}
}
