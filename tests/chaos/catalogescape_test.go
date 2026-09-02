// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package chaos

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/engine"
)

// Port rules are read off a module's manifest, and a module outside this
// daemon's catalog has none — runner and MCP drops live elsewhere, so "unknown
// here" is not "invalid". That exemption used to cover fan-in too, which meant
// the one wiring rule that needs no port list was skipped for exactly the steps
// the canvas can't see: 300 wires into one input were stored, run, and reduced
// to whichever value was walked last.
func TestCatalogLessModule_StillObeysFanIn(t *testing.T) {
	h := newHarness(t)

	fanIn := func(id string, n int) core.Graph {
		nodes := []core.Node{{ID: "sink", Module: "runner.mystery_step"}}
		var edges []core.Edge
		for i := range n {
			src := fmt.Sprintf("src%d", i)
			nodes = append(nodes, textNode(src, fmt.Sprintf("v%d", i)))
			edges = append(edges, core.Edge{From: src, FromPort: "out", To: "sink", ToPort: "in"})
		}
		return graph(id, nodes, edges)
	}

	mustRefuse := map[string]core.Graph{
		"300 wires into one input of a runner drop": fanIn("unknownfanin", 300),
		"two wires into one input of an MCP tool": graph("mcpfanin",
			[]core.Node{textNode("a", "x"), textNode("b", "y"), {ID: "t", Module: "mcp.some_tool"}},
			[]core.Edge{
				{From: "a", FromPort: "out", To: "t", ToPort: "in"},
				{From: "b", FromPort: "out", To: "t", ToPort: "in"},
			}),
	}
	for name, g := range mustRefuse {
		t.Run(name, func(t *testing.T) {
			if _, err := h.svc.SaveGraph(t.Context(), h.p, g); err != nil {
				t.Logf("refused at save: %v", err)
				return
			}
			st, err := h.submit(g, 30*time.Second)
			if err != nil {
				t.Logf("refused at submit: %v", err)
				return
			}
			t.Errorf("FINDING: stored and RAN this wiring (status %s)", st)
		})
	}

	// Still accepted, and deliberately so: with no manifest there is no port
	// list to judge, so a port name we don't recognize is the drop's business.
	// One wire per port is all the data model promises to carry.
	mustAccept := map[string]core.Graph{
		"a port name that is pure noise": graph("noise",
			[]core.Node{textNode("a", "x"), {ID: "b", Module: "mcp.some_tool"}},
			[]core.Edge{{From: "a", FromPort: "out", To: "b", ToPort: strings.Repeat("💥", 64)}}),
		"1000 wires spread over 1000 invented ports": func() core.Graph {
			nodes := []core.Node{textNode("a", "x"), {ID: "b", Module: "runner.mystery_step"}}
			var edges []core.Edge
			for i := range 1000 {
				edges = append(edges, core.Edge{From: "a", FromPort: "out", To: "b", ToPort: fmt.Sprintf("p%d", i)})
			}
			return graph("invented", nodes, edges)
		}(),
	}
	for name, g := range mustAccept {
		t.Run(name, func(t *testing.T) {
			if _, err := h.svc.SaveGraph(t.Context(), h.p, g); err != nil {
				t.Errorf("refused a wiring that has to stay legal: %v", err)
				return
			}
			st, err := h.submit(g, 30*time.Second)
			if err != nil {
				t.Errorf("refused at submit: %v", err)
				return
			}
			t.Logf("status=%s (the step itself fails: no runner here)", st)
		})
	}
}

// Why fan-in has to be validated even without a manifest: AssembleInput writes
// each wire to the same map key, so the step receives ONE value — whichever
// edge was walked last — and nothing anywhere reports the rest as dropped.
func TestCatalogLessModule_AssemblesOneValuePerPort(t *testing.T) {
	const wires = 300
	nodes := []core.Node{{ID: "sink", Module: "runner.mystery_step"}}
	var edges []core.Edge
	prior := map[string]core.Result{}
	for i := range wires {
		id := fmt.Sprintf("src%d", i)
		nodes = append(nodes, textNode(id, fmt.Sprintf("v%d", i)))
		edges = append(edges, core.Edge{From: id, FromPort: "out", To: "sink", ToPort: "in"})
		prior[id] = core.Result{Status: core.StatusOK, Output: map[string]core.Ref{
			"out": {Inline: fmt.Sprintf("v%d", i)},
		}}
	}
	g := graph("silentdrop", nodes, edges)

	// core.Manifest{} is what the daemon has for a module outside its catalog.
	input := engine.AssembleInput(g, "sink", core.Manifest{}, prior)
	if len(input) != 1 {
		t.Errorf("AssembleInput delivered %d values for %d wires; the fan-in rule is calibrated to it keeping exactly one",
			len(input), wires)
	}
	if err := core.ValidateRuntime(g, engine.Default.Manifests()); err == nil {
		t.Errorf("FINDING: a wiring that loses %d of %d values validated clean", wires-1, wires)
	} else {
		t.Logf("delivered %d of %d values, and the wiring is refused: %v", len(input), wires, firstLine(err))
	}
}

// Wirings the model must never accept, as a guard.
func TestWiringsThatMustStayRefused(t *testing.T) {
	h := newHarness(t)
	delayNode := func(id string) core.Node {
		return core.Node{ID: id, Module: "delay", Params: map[string]any{"ms": 0}}
	}

	passFanIn := func(n int) core.Graph {
		nodes := []core.Node{delayNode("sink")}
		var edges []core.Edge
		for i := range n {
			id := fmt.Sprintf("src%d", i)
			nodes = append(nodes, textNode(id, "x"))
			edges = append(edges, core.Edge{From: id, FromPort: "out", To: "sink", ToPort: "pass"})
		}
		return graph("passfanin", nodes, edges)
	}

	cases := map[string]core.Graph{
		"200 wires into the pass pin": passFanIn(200),
		"wire into a trigger's output port": graph("intotrigger",
			[]core.Node{textNode("a", "x"), {ID: "t", Module: "webhook_input"}},
			[]core.Edge{{From: "a", FromPort: "out", To: "t", ToPort: "body"}}),
		"wire into a trigger's pass pin (triggers have none)": graph("triggerpass",
			[]core.Node{textNode("a", "x"), {ID: "t", Module: "cron_trigger"}},
			[]core.Edge{{From: "a", FromPort: "out", To: "t", ToPort: "pass"}}),
		"cycle laundered through a loop body pin": graph("bodycycle",
			[]core.Node{
				{ID: "loop", Module: "for_each"},
				delayNode("body"),
			},
			[]core.Edge{
				{From: "loop", FromPort: "body", To: "body", ToPort: "pass"},
				{From: "body", FromPort: "pass", To: "loop", ToPort: "items"},
			}),
		"cycle where every wire is a fallback": graph("fbcycle",
			[]core.Node{b64Node("a"), b64Node("b")},
			[]core.Edge{
				{From: "a", FromPort: "out", To: "b", ToPort: "in", OnError: core.OnErrorFallback},
				{From: "b", FromPort: "out", To: "a", ToPort: "in", OnError: core.OnErrorFallback},
			}),
		"65 wires into a variadic input with no declared max": func() core.Graph {
			nodes := []core.Node{{ID: "m", Module: "merge"}}
			var edges []core.Edge
			for i := range core.DefaultMaxVariadicFanIn + 1 {
				id := fmt.Sprintf("src%d", i)
				nodes = append(nodes, textNode(id, "x"))
				edges = append(edges, core.Edge{From: id, FromPort: "out", To: "m", ToPort: "items"})
			}
			return graph("variadic", nodes, edges)
		}(),
	}

	for name, g := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := h.svc.SaveGraph(t.Context(), h.p, g); err != nil {
				t.Logf("refused at save: %v", err)
				return
			}
			st, err := h.submit(g, 30*time.Second)
			if err != nil {
				t.Logf("refused at submit: %v", err)
				return
			}
			t.Errorf("FINDING: stored AND run (status %s)", st)
		})
	}
}
