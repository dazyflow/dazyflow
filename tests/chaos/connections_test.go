// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package chaos

import (
	"bytes"
	"fmt"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/engine"
)

// FINDING: port-level rules are advisory only. core.ValidateWithManifests
// (fan-in on non-variadic inputs, port existence, MIME, on_error values) runs
// in the editor/AI gate and in Engine.RunGraph — but the daemon's save and
// submit paths call only core.Validate, and the daemon executes node-by-node,
// never through Engine.RunGraph. So every wiring below is stored and RUN.
//
// Each subtest asserts the wiring is refused somewhere between SaveGraph and
// a terminal run status. Today most are accepted and some silently succeed.
func TestIllegalWiring_IsRefusedNotRun(t *testing.T) {
	manifests := engine.Default.Manifests()

	fanIn := func(n int) core.Graph {
		nodes := []core.Node{{ID: "sink", Module: "base64", Params: map[string]any{"mode": "encode"}}}
		var edges []core.Edge
		for i := 0; i < n; i++ {
			id := fmt.Sprintf("src%d", i)
			nodes = append(nodes, core.Node{ID: id, Module: "text", Params: map[string]any{"text": "x"}})
			edges = append(edges, core.Edge{From: id, FromPort: "out", To: "sink", ToPort: "in"})
		}
		return graph("fanin", nodes, edges)
	}
	twoNodes := func(from, to core.Node, e core.Edge) core.Graph {
		return graph("pair", []core.Node{from, to}, []core.Edge{e})
	}
	text := core.Node{ID: "a", Module: "text", Params: map[string]any{"text": "x"}}
	b64 := core.Node{ID: "b", Module: "base64", Params: map[string]any{"mode": "encode"}}

	cases := map[string]core.Graph{
		// The headline: nothing caps how many wires reach a single-value
		// input. AssembleInput keeps the last edge it walks, so 200 sources
		// silently collapse to one arbitrary value.
		"200 wires into one non-variadic input": fanIn(200),
		"duplicate identical wires": graph("dupe", []core.Node{text, b64}, []core.Edge{
			{From: "a", FromPort: "out", To: "b", ToPort: "in"},
			{From: "a", FromPort: "out", To: "b", ToPort: "in"},
		}),
		"edge into a port that does not exist": twoNodes(text, b64,
			core.Edge{From: "a", FromPort: "out", To: "b", ToPort: "no_such_port"}),
		"edge out of an input port": twoNodes(
			core.Node{ID: "a", Module: "base64", Params: map[string]any{"mode": "encode"}}, b64,
			core.Edge{From: "a", FromPort: "in", To: "b", ToPort: "in"}),
		// A typo'd policy falls through the engine's switch as "abort": the
		// failure handling the author asked for silently never happens.
		"garbage on_error": twoNodes(text, b64,
			core.Edge{From: "a", FromPort: "out", To: "b", ToPort: "in", OnError: "fallbcak"}),
		"MIME-incompatible wire": graph("mime",
			[]core.Node{
				{ID: "src", Module: "text", Params: map[string]any{"text": "[1,2,3]"}},
				{ID: "loop", Module: "for_each"},
				{ID: "sink", Module: "base64", Params: map[string]any{"mode": "encode"}},
			},
			[]core.Edge{
				{From: "src", FromPort: "out", To: "loop", ToPort: "items"},
				{From: "loop", FromPort: "body", To: "sink", ToPort: "in"},
			}),
	}

	for name, g := range cases {
		t.Run(name, func(t *testing.T) {
			// The editor gate sees every one of these as an error, which is
			// what makes the run path's silence a gap rather than a design.
			issues := core.ValidateGraphFull(g, manifests)
			var editorErrors int
			for _, i := range issues {
				if i.Severity == core.LintError {
					editorErrors++
					t.Logf("editor gate: [%s] %s", i.Code, i.Message)
				}
			}
			if editorErrors == 0 {
				t.Fatalf("case is not actually illegal — editor gate reports no error")
			}

			hs := newHarness(t)
			var saveErr, submitErr error
			var status core.JobStatus
			withinDeadline(t, "save+submit", 90*time.Second, func() {
				_, saveErr = hs.svc.SaveGraph(t.Context(), hs.p, g)
				status, submitErr = hs.submit(g, 30*time.Second)
			})
			t.Logf("SaveGraph=%v SubmitGraph=%v status=%q", saveErr, submitErr, status)
			if saveErr == nil && submitErr == nil && status == core.JobStatusSucceeded {
				t.Errorf("graph the editor rejects was saved, run, and reported SUCCEEDED")
			}
			if saveErr == nil && submitErr == nil {
				t.Errorf("graph the editor rejects was accepted by both save and submit gates (ran to %q)", status)
			}
		})
	}
}

// A module missing from the catalog is deliberately NOT refused: a tenant's
// runner and MCP drops live outside the default palette, so "unknown here"
// isn't "invalid". It has to fail at the step, though — cleanly, not by
// hanging the run.
func TestUnknownModule_FailsAtTheStep(t *testing.T) {
	hs := newHarness(t)
	g := graph("ghost", []core.Node{{ID: "a", Module: "definitely_not_a_drop"}}, nil)
	if _, err := hs.svc.SaveGraph(t.Context(), hs.p, g); err != nil {
		t.Errorf("SaveGraph refused an unresolvable module: %v", err)
	}
	status, err := hs.submit(g, 30*time.Second)
	if status != core.JobStatusFailed {
		t.Errorf("status=%q err=%v, want failed at the step", status, err)
	}
}

// analyzeDependent logged one "waiting: predecessor …" line per dependent
// per completion, so a wide fan-in buried the log: the 200-wire graph above
// wrote thousands of lines in under a second, none of them actionable ("not
// ready yet" is the normal state of every step before its turn). The trace
// is now behind DAZYFLOW_DEBUG_DISPATCH.
func TestWideFanIn_DoesNotFloodTheLog(t *testing.T) {
	var logged bytes.Buffer
	hs := newHarnessLogging(t, log.New(&logged, "", 0))

	// The widest legal fan-in, tiered: core.DefaultMaxVariadicFanIn caps one
	// pin, so four full Merge pins converge on a fifth. 260 steps, and every
	// completion still makes the dispatcher re-evaluate a step with dozens of
	// predecessors — the shape that produced one log line per dependent per
	// completion.
	const tiers, width = 4, core.DefaultMaxVariadicFanIn
	nodes := []core.Node{{ID: "sink", Module: "merge"}}
	var edges []core.Edge
	for tier := 0; tier < tiers; tier++ {
		mid := fmt.Sprintf("mid%d", tier)
		nodes = append(nodes, core.Node{ID: mid, Module: "merge"})
		edges = append(edges, core.Edge{From: mid, FromPort: "out", To: "sink", ToPort: "items"})
		for i := 0; i < width; i++ {
			id := fmt.Sprintf("src%d_%d", tier, i)
			nodes = append(nodes, core.Node{ID: id, Module: "text", Params: map[string]any{"text": "x"}})
			edges = append(edges, core.Edge{From: id, FromPort: "out", To: mid, ToPort: "items"})
		}
	}
	status, err := hs.submit(graph("fanin", nodes, edges), 60*time.Second)
	if status != core.JobStatusSucceeded {
		t.Fatalf("status=%q err=%v", status, err)
	}
	lines := strings.Count(logged.String(), "\n")
	t.Logf("worker wrote %d log lines for a %d-step fan-in", lines, len(nodes))
	if lines > 20 {
		t.Errorf("worker wrote %d log lines for one run — a wide fan-in still floods the log", lines)
	}
}

// core.DefaultMaxVariadicFanIn (64) bounds a variadic input only when the port
// declares no Max of its own — so the drop that DECLARES the port picked its
// own ceiling, and a manifest is not always ours to trust. A remote runner's
// arrives over gRPC and its max is taken verbatim (engine.portFromPB does no
// clamping), as does an MCP host's and a web-API catalog's. A port declaring
// max=1000000 therefore put fan-in back exactly where it was before the
// default existed, bounded only by the 5000-connection cap — and for precisely
// the steps outside the default palette, which is the same blind spot
// TestCatalogLessModule_StillObeysFanIn was written about.
//
// Every wire is a value the run assembles and stores, so "the manifest says so"
// cannot mean unbounded.
func TestManifestDeclaredFanIn_IsClamped(t *testing.T) {
	const wires = 3000
	declared := 1_000_000

	nodes := []core.Node{{ID: "sink", Module: "runner.collector"}}
	edges := make([]core.Edge, 0, wires)
	for i := range wires {
		id := fmt.Sprintf("src%d", i)
		nodes = append(nodes, textNode(id, "x"))
		edges = append(edges, core.Edge{From: id, FromPort: "out", To: "sink", ToPort: "items"})
	}

	// A runner's manifest, as portFromPB would build it from the wire.
	manifests := map[string]core.Manifest{
		"text":             {ID: "text", Outputs: []core.Port{{Port: "out"}}},
		"runner.collector": {ID: "runner.collector", Inputs: []core.Port{{Port: "items", Variadic: true, Max: &declared}}},
	}

	err := core.ValidateRuntime(graph("maxbypass", nodes, edges), manifests)
	if err == nil {
		t.Errorf("FINDING: %d wires into one variadic input were accepted because the "+
			"manifest declared max=%d — a drop cannot be allowed to raise its own fan-in ceiling",
			wires, declared)
	} else {
		t.Logf("refused: %v", firstLine(err))
	}
}
