// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package chaos

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
)

// The other ceilings all COUNT things — nodes, wires, frames, waypoints per
// wire — and none of them weighed the graph. Params, labels, env and frame
// titles are free-form, so a flow inside every count ceiling reached 156 MiB
// (208 MiB of run records), bounded only by the 200 MiB request cap that exists
// for file uploads.
func TestGraphBytes_AreCapped(t *testing.T) {
	h := newHarness(t)
	junk := strings.Repeat("x", 64<<10) // one allocation, counted per use

	nodes := []core.Node{textNode("src", "x")}
	var edges []core.Edge
	prev, prevPort := "src", "out"
	for i := range 500 {
		id := fmt.Sprintf("hop%03d", i)
		nodes = append(nodes, core.Node{
			ID: id, Module: "delay", Params: map[string]any{"ms": 0, "note": junk},
			Label: junk, Env: map[string]string{"JUNK": junk},
		})
		edges = append(edges, core.Edge{From: prev, FromPort: prevPort, To: id, ToPort: "pass"})
		prev, prevPort = id, "pass"
	}
	g := graph("fatgraph", nodes, edges)
	for range core.MaxGraphFrames {
		g.Frames = append(g.Frames, core.Frame{ID: "f", Title: junk})
	}
	payload, _ := json.Marshal(g)

	if _, err := h.svc.SaveGraph(t.Context(), h.p, g); err == nil {
		st, _ := h.submit(g, 5*time.Minute)
		t.Errorf("FINDING: a %d MiB flow inside every count ceiling was stored and run (status %s, %d MiB of run records)",
			len(payload)>>20, st, h.storedBytes("fatgraph")>>20)
		return
	} else {
		t.Logf("%d MiB refused at save: %v", len(payload)>>20, firstLine(err))
	}

	// A flow of ordinary weight still saves and runs.
	slim := graph("slimgraph", nodes[:20], edges[:19])
	for i := range slim.Nodes {
		slim.Nodes[i].Label, slim.Nodes[i].Env = "", nil
		if slim.Nodes[i].Params != nil {
			delete(slim.Nodes[i].Params, "note")
		}
	}
	if _, err := h.svc.SaveGraph(t.Context(), h.p, slim); err != nil {
		t.Fatalf("an ordinary flow was refused: %v", err)
	}
	st, err := h.submit(slim, time.Minute)
	if err != nil || st != core.JobStatusSucceeded {
		t.Errorf("ordinary flow: status=%s err=%v", st, err)
	}
}

// Editor metadata is bounded per wire (256 waypoints) AND in total: per-wire
// alone left 1.28M waypoints reachable at the connection ceiling, ~21 MiB of
// routing knots copied into every run record and re-parsed each dispatch pass.
func TestWaypointTotal_IsCapped(t *testing.T) {
	h := newHarness(t)
	wp := make([]core.Position, core.MaxEdgeWaypoints)
	for i := range wp {
		wp[i] = core.Position{X: float64(i), Y: float64(i)}
	}
	// A chain threaded through the pass pin: constant-size payload, so the only
	// thing growing is the metadata on each wire.
	build := func(id string, hops int, waypoints []core.Position) core.Graph {
		nodes := []core.Node{textNode("src", "x")}
		var edges []core.Edge
		prev, prevPort := "src", "out"
		for i := range hops {
			hop := fmt.Sprintf("hop%03d", i)
			nodes = append(nodes, core.Node{ID: hop, Module: "delay", Params: map[string]any{"ms": 0}})
			edges = append(edges, core.Edge{
				From: prev, FromPort: prevPort, To: hop, ToPort: "pass", Waypoints: waypoints,
			})
			prev, prevPort = hop, "pass"
		}
		return graph(id, nodes, edges)
	}

	// 500 wires x 256 waypoints = 128,000, well past the total.
	bomb := build("wpbomb", 500, wp)
	if _, err := h.svc.SaveGraph(t.Context(), h.p, bomb); err == nil {
		st, _ := h.submit(bomb, 5*time.Minute)
		t.Errorf("FINDING: %d waypoints stored and run (status %s, %d KiB of run records)",
			500*core.MaxEdgeWaypoints, st, h.storedBytes("wpbomb")>>10)
	} else {
		t.Logf("%d waypoints refused at save: %v", 500*core.MaxEdgeWaypoints, firstLine(err))
	}

	// Hand-routed wiring inside the total still saves and runs.
	hops := core.MaxGraphWaypoints / core.MaxEdgeWaypoints
	routed := build("wprouted", hops, wp)
	if _, err := h.svc.SaveGraph(t.Context(), h.p, routed); err != nil {
		t.Fatalf("waypoints inside the total were refused: %v", err)
	}
	st, err := h.submit(routed, 2*time.Minute)
	if err != nil || st != core.JobStatusSucceeded {
		t.Errorf("%d hand-routed wires: status=%s err=%v", hops, st, err)
	}
}

// Nesting: encoding/json refuses past 10k levels, so the question is whether
// everything that walks a param or a value survives just under that — the
// linter, the template resolver, the size walk, the redactor.
func TestDeepNesting_SurvivesTheRunPath(t *testing.T) {
	h := newHarness(t)

	nested := func(depth int, inner string) any {
		var v any = inner
		for range depth {
			v = []any{v}
		}
		return v
	}

	// Deep enough to exercise every walk, inside what the graph-size walk
	// measures (past that a param counts as over budget and is refused).
	const deep = 48
	cases := map[string]core.Graph{
		"deeply nested param on a step": graph("deepparam",
			[]core.Node{{ID: "a", Module: "delay", Params: map[string]any{"ms": 0, "junk": nested(deep, "x")}}}, nil),
		"deeply nested param holding a template reference": graph("deeptmpl",
			[]core.Node{{ID: "a", Module: "delay", Params: map[string]any{"ms": 0, "junk": nested(deep, "${upstream.a.out}")}}}, nil),
		"9000-deep JSON parsed at run time": graph("deepvalue",
			[]core.Node{
				textNode("src", strings.Repeat("[", 9000)+`"x"`+strings.Repeat("]", 9000)),
				{ID: "p", Module: "parse_json"},
			},
			[]core.Edge{{From: "src", FromPort: "out", To: "p", ToPort: "in"}}),
	}

	for name, g := range cases {
		t.Run(name, func(t *testing.T) {
			withinDeadline(t, name, 90*time.Second, func() {
				if _, err := h.svc.SaveGraph(t.Context(), h.p, g); err != nil {
					t.Errorf("refused at save: %v", err)
					return
				}
				st, err := h.submit(g, 60*time.Second)
				if err != nil {
					t.Logf("refused at submit: %v", err)
					return
				}
				t.Logf("status=%s", st)
				if st == statusHung {
					t.Errorf("FINDING: never reached a terminal status")
				}
			})
		})
	}

	// Past the depth the size walk covers, a param is refused rather than
	// accepted unmeasured — nothing real nests that deeply.
	absurd := graph("deepabsurd",
		[]core.Node{{ID: "a", Module: "delay", Params: map[string]any{"ms": 0, "junk": nested(9000, "x")}}}, nil)
	if _, err := h.svc.SaveGraph(t.Context(), h.p, absurd); err == nil {
		t.Error("FINDING: a 9000-deep param was stored")
	} else {
		t.Logf("9000-deep param refused at save: %v", firstLine(err))
	}
}
