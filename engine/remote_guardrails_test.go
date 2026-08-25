// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"
	"strings"
	"testing"

	nodepb "git.sr.ht/~klahr/dazyflow/api/gen/node"
	"git.sr.ht/~klahr/dazyflow/core"
)

// The three guard rails around tenant runners: the reserved namespace, what a
// tenant can see in its palette, and the refusal to send a file path to a
// machine that cannot read it.

// ---- the reserved namespace -------------------------------------------

// A built-in must never be able to take an id an org's runner already uses.
// Refusing at registration means the collision is a build-time error in this
// repo rather than a silent change to what an existing flow runs on upgrade.
func TestRegistry_RefusesTheRunnerNamespace(t *testing.T) {
	reg := NewRegistry()
	err := reg.Register(NativeDrop{
		Manifest: core.Manifest{
			ID:       RunnerDropID("box", "fetch"),
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

// The tenant is deliberately absent from the id: putting it there would bake a
// tenant name into saved graphs, so forking a flow or moving it between
// workspaces would dangle every runner reference.
func TestRunnerDropID_CarriesNoTenant(t *testing.T) {
	id := RunnerDropID("box", "fetch")
	if !strings.HasPrefix(id, RunnerNamespace) {
		t.Errorf("id = %q, want the reserved prefix", id)
	}
	if !strings.Contains(id, "box") || !strings.Contains(id, "fetch") {
		t.Errorf("id = %q, want it to name the runner and the drop", id)
	}
	for _, tenant := range []string{"acme", "globex", "usr_deadbeef"} {
		if strings.Contains(id, tenant) {
			t.Errorf("id = %q contains a tenant name", id)
		}
	}
}

// ---- palette scoping --------------------------------------------------

// A tenant's palette must contain its own runner's drops and no one else's.
// Seeing another org's step would be a broken entry AND a disclosure: it tells
// you a runner by that name exists somewhere.
func TestManifestsForTenant_ShowsOnlyThatTenantsRunners(t *testing.T) {
	c := NewRemoteCatalog()
	defer c.Close()
	fakeRemote(c, "acme", RunnerDropID("box", "fetch"))
	fakeRemote(c, "globex", RunnerDropID("till", "settle"))

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
	if _, ok := acme[RunnerDropID("box", "fetch")]; !ok {
		t.Error("acme cannot see its own runner's drop")
	}
	if _, ok := acme[RunnerDropID("till", "settle")]; ok {
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
	fakeRemote(c, "acme", RunnerDropID("box", "fetch"))
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
	if _, ok := all[RunnerDropID("box", "fetch")]; ok {
		t.Error("the unscoped map leaked a tenant's runner drop")
	}
}

// ---- the inline-only bound --------------------------------------------

// Ref.Ref is a path on the DAEMON's disk. Sending it to a runner on another
// machine would fail inside the org's own code, reporting a missing file the
// org would reasonably read as their bug. Refuse here, naming the real cause.
func TestExecute_RefusesAFileRefBeforeDialling(t *testing.T) {
	// No connection at all: if the refusal did not happen first, this would
	// fail on a nil client instead, so the test also proves the ordering.
	tr := &RemoteTransport{
		Descriptor: RemoteDescriptor{ID: "box", Tenant: "acme", Endpoint: "127.0.0.1:1"},
		manifest:   core.Manifest{ID: RunnerDropID("box", "fetch")},
		dropID:     "fetch",
	}
	_, err := tr.Execute(context.Background(), core.Job{
		ID:    "j1",
		Input: map[string]core.Ref{"in": {Ref: "invoices/march.csv", MIME: "text/csv"}},
	}, nil)
	if err == nil {
		t.Fatal("a job carrying a file path was sent to a runner")
	}
	// The message has to name the cause, the port, and the fix — this is the
	// error a flow author reads when their file-wired step stops working.
	for _, want := range []string{"in", "invoices/march.csv", "runner", "value"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, missing %q", err.Error(), want)
		}
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
	tr, ok := c.Get("acme", RunnerDropID("box", "fetch"))
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
