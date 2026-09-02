// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package chaos

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/daemon"
	"github.com/dazyflow/dazyflow/engine"
)

// submitOpts is harness.submit with the trigger-chain depth a trigger
// endpoint would have stamped on the run.
func (h *harness) submitOpts(g core.Graph, opts daemon.SubmitOpts, budget time.Duration) (string, core.JobStatus, error) {
	h.t.Helper()
	runID, err := h.svc.SubmitGraphOpts(h.t.Context(), h.p, g, opts)
	if err != nil {
		return "", "", err
	}
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		rec, rerr := h.jobs.Get(context.Background(), runID)
		if rerr == nil && core.IsTerminalStatus(rec.Status) {
			return runID, rec.Status, nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return runID, statusHung, nil
}

// childRuns returns every graph-run record that was spawned as a subgraph
// child (ParentNodeRecID set).
func (h *harness) childRuns() []core.JobRecord {
	h.t.Helper()
	recs, err := h.jobs.ListGraphRuns(context.Background(), core.ListGraphRunsOpts{Limit: 100000})
	if err != nil {
		h.t.Fatalf("list runs: %v", err)
	}
	var out []core.JobRecord
	for _, r := range recs {
		if r.ParentNodeRecID != "" {
			out = append(out, r)
		}
	}
	return out
}

// A subgraph child run must inherit its parent's trigger-chain depth.
//
// The two runaway-recursion guards divide the space: subgraph nesting is
// bounded by walking ParentNodeRecID (maxSubgraphDepth), and a flow that
// calls its OWN trigger URL is bounded by core.MaxTriggerChainDepth carried
// on the run record and re-stamped onto the outbound request by the HTTP
// drop. Neither covers a chain that ALTERNATES between the two.
//
// submitGraphWithParent builds the child's JobRecord without copying
// TriggerDepth, so every subgraph hop resets the trigger counter to zero:
//
//	A (webhook trigger, depth d)
//	  └─ subgraph step → B   (child run, depth 0  ← the reset)
//	       └─ HTTP step → POST A's own trigger URL, header depth 0+1 = 1
//	            └─ A (depth 1) → subgraph B (depth 0) → ...
//
// The trigger counter never climbs to MaxTriggerChainDepth, the subgraph
// lineage walk never sees more than one level (each webhook run is a fresh
// top-level tree), and the per-tree fan-out budget is re-keyed on every new
// root. The loop runs forever at whatever rate the HTTP step sustains.
func TestTriggerChainDepth_SurvivesASubgraphHop(t *testing.T) {
	hs := newHarness(t)
	hs.save(graph("kid", []core.Node{
		{ID: "n", Module: "delay", Params: map[string]any{"ms": 0}},
	}, nil))

	parent := graph("parent",
		[]core.Node{
			{ID: "start", Module: "delay", Params: map[string]any{"ms": 0}},
			{ID: "call", Module: "subgraph", Params: map[string]any{"graph_id": "kid"}},
		},
		[]core.Edge{{From: "start", FromPort: "pass", To: "call", ToPort: "in"}})
	hs.save(parent)

	// One short of the cap: a run this deep in a trigger chain may do one
	// more hop, and no more.
	const parentDepth = core.MaxTriggerChainDepth - 1
	runID, status, err := hs.submitOpts(parent, daemon.SubmitOpts{TriggerDepth: parentDepth}, 60*time.Second)
	if err != nil {
		t.Fatalf("submit parent: %v", err)
	}
	t.Logf("parent run %s status=%q", runID, status)

	kids := hs.childRuns()
	if len(kids) == 0 {
		t.Fatalf("no subgraph child run was created (parent status %q)", status)
	}
	for _, k := range kids {
		t.Logf("child run %s graph=%s trigger_depth=%d", k.ID, k.GraphID, k.TriggerDepth)
		if k.TriggerDepth < parentDepth {
			t.Errorf("child run %s of %q carries trigger_depth=%d, parent had %d — "+
				"a subgraph hop resets the trigger-chain counter, so "+
				"flow-A → subgraph-B → HTTP-trigger-A never reaches the cap",
				k.ID, k.GraphID, k.TriggerDepth, parentDepth)
		}
	}
}

// A dynamic-port step must not be a hole in the fan-in rule.
//
// core.validateManifests skips every port check on an edge touching a
// DynamicPorts module — today only `subgraph`, whose real ports are named by
// its input_map param. The skip is understandable for port EXISTENCE and MIME
// (the manifest can't know the names), but it also drops the fan-in rule, and
// that one needs no manifest at all: two wires into one input port is
// unrepresentable whatever the port is called. AssembleInput keeps the last
// edge it walks, so the other 199 values are silently discarded — the exact
// failure TestIllegalWiring_IsRefusedNotRun fixed for ordinary steps.
func TestDynamicPortsStep_FanInIsBounded(t *testing.T) {
	hs := newHarness(t)
	hs.save(graph("kid", []core.Node{
		{ID: "n", Module: "delay", Params: map[string]any{"ms": 0}},
	}, nil))

	const wires = 200
	nodes := []core.Node{{ID: "call", Module: "subgraph", Params: map[string]any{
		"graph_id":  "kid",
		"input_map": map[string]any{"in": "n"},
	}}}
	var edges []core.Edge
	for i := 0; i < wires; i++ {
		id := fmt.Sprintf("src%d", i)
		nodes = append(nodes, core.Node{ID: id, Module: "text", Params: map[string]any{"text": fmt.Sprintf("v%d", i)}})
		edges = append(edges, core.Edge{From: id, FromPort: "out", To: "call", ToPort: "in"})
	}
	g := graph("dynfan", nodes, edges)

	if err := core.ValidateRuntime(g, engine.Default.Manifests()); err != nil {
		t.Logf("refused by ValidateRuntime: %v", err)
		return
	}
	_, err := hs.svc.SubmitGraph(t.Context(), hs.p, g)
	if err != nil {
		t.Logf("refused at submit: %v", err)
		return
	}
	t.Errorf("%d wires into one input port of a dynamic-port step were accepted and run; "+
		"199 of the values are silently dropped", wires)
}

// No variadic input port in the whole catalog declares a Max, so the only
// ceiling on fan-in is the 5000-connection graph cap. Two steps and 4000
// duplicate wires between them is a legal graph; every wire becomes its own
// items[N] entry the run has to assemble, hold and store.
//
// The case is small enough to be typed by hand in the editor (drag the same
// wire repeatedly) and, unlike the non-variadic fan-in, nothing anywhere
// says it is wrong.
func TestVariadicFanIn_IsBounded(t *testing.T) {
	const wires = 400 // well past core.DefaultMaxVariadicFanIn
	hs := newHarness(t)

	// Distinct sources, so the fan-in ceiling is what has to refuse this and
	// not the duplicate-wire rule.
	nodes := []core.Node{{ID: "sink", Module: "merge"}}
	edges := make([]core.Edge, 0, wires)
	for i := 0; i < wires; i++ {
		id := fmt.Sprintf("src%d", i)
		nodes = append(nodes, core.Node{ID: id, Module: "text", Params: map[string]any{"text": "payload"}})
		edges = append(edges, core.Edge{From: id, FromPort: "out", To: "sink", ToPort: "items"})
	}
	g := graph("variadicbomb", nodes, edges)

	if err := core.ValidateRuntime(g, engine.Default.Manifests()); err != nil {
		t.Logf("refused by ValidateRuntime: %v", err)
		return
	}
	start := time.Now()
	status, err := hs.submit(g, 90*time.Second)
	t.Logf("%d wires into one variadic port: status=%q err=%v elapsed=%s",
		wires, status, err, time.Since(start))
	if err == nil && status != statusHung {
		t.Errorf("%d wires into a single variadic input were accepted and run (%q) — "+
			"no variadic port declares a Max, so nothing but the graph connection cap bounds fan-in",
			wires, status)
	}
}

// The graph caps count nodes and connections. Nothing counts the editor-only
// metadata that rides along in the same JSON: an edge's Waypoints list and
// the graph's Frames are both unbounded. The payload is marshalled once per
// run record and re-parsed by the worker on every dispatch pass, so the cost
// is paid over and over for bytes the engine never reads.
func TestEditorMetadata_IsCapped(t *testing.T) {
	base := func() ([]core.Node, []core.Edge) {
		return []core.Node{
				{ID: "a", Module: "text", Params: map[string]any{"text": "x"}},
				{ID: "b", Module: "base64", Params: map[string]any{"mode": "encode"}},
			},
			[]core.Edge{{From: "a", FromPort: "out", To: "b", ToPort: "in"}}
	}

	t.Run("waypoints on one wire", func(t *testing.T) {
		hs := newHarness(t)
		nodes, edges := base()
		way := make([]core.Position, core.MaxEdgeWaypoints+1)
		for i := range way {
			way[i] = core.Position{X: float64(i), Y: float64(i)}
		}
		edges[0].Waypoints = way
		g := graph("waypointbomb", nodes, edges)
		if _, err := hs.svc.SubmitGraph(t.Context(), hs.p, g); err != nil {
			t.Logf("refused: %v", err)
			return
		}
		t.Errorf("a 2-node graph carrying %d edge waypoints passed both graph caps", len(way))
	})

	t.Run("frames on the graph", func(t *testing.T) {
		hs := newHarness(t)
		nodes, edges := base()
		g := graph("framebomb", nodes, edges)
		g.Frames = make([]core.Frame, core.MaxGraphFrames+1)
		for i := range g.Frames {
			g.Frames[i] = core.Frame{ID: fmt.Sprintf("f%d", i), Title: "note", Width: 100, Height: 100}
		}
		if _, err := hs.svc.SubmitGraph(t.Context(), hs.p, g); err != nil {
			t.Logf("refused: %v", err)
			return
		}
		t.Errorf("a 2-node graph carrying %d frames passed both graph caps", len(g.Frames))
	})
}

// The doubling bomb rebuilt out of Merge steps, with no templates involved.
//
// core.MaxValueBytes exists because an uncapped compounding value killed the
// process with `fatal error: out of memory` inside the resolver — a runtime
// throw no recover catches, so it took every tenant's runs down with it
// (TestOOM_DoublingTemplateBomb). Both guards that enforce it — the engine's
// per-value check (core.RefTooLarge) and the worker's per-run budget
// (resultStateBytes) — measure with core.ApproxValueSize.
//
// `merge` emits Inline: []core.Ref — a slice of STRUCTS. Charged 8 bytes per
// element and never walked, an 18 MB value measured as 16 bytes and sailed
// past a 1 MiB ceiling; 21 steps stored a gigabyte. core.refSize now walks a
// Ref for its strings and its inline payload, and the reflect arm walks a
// struct's exported fields rather than charging it a word, so the fan-out
// below is refused at the step that crosses the limit.
//
// The wiring is ordinary and legal throughout — fan-in 2 on a variadic pin,
// every wire distinct — so nothing else is in a position to refuse it.
func TestMergeChain_HitsTheValueCeiling(t *testing.T) {
	const seedKiB = 1
	hops := 15 // 2^14 KiB ≈ 16 MiB stored, from a 1 KiB seed
	if os.Getenv("DAZYFLOW_CHAOS_OOM") != "" {
		hops = 20 // ≈ 512 MiB stored, to measure the real memory multiplier
	}

	// Squeeze both ceilings far below what this flow stores: with a working
	// guard the run must fail with value_too_large or run_state_too_large.
	defer core.SetMaxValueBytes(1 << 20)()    // 1 MiB per value
	defer core.SetMaxRunStateBytes(4 << 20)() // 4 MiB per run

	hs := newHarness(t)
	seed := make([]byte, seedKiB<<10)
	for i := range seed {
		seed[i] = 'x'
	}

	// Two Merge steps per hop, each fed by BOTH of the previous hop's — every
	// wire distinct, every fan-in 2, so nothing but the value ceiling is in a
	// position to refuse it. Each hop still holds twice what the last one did.
	nodes := []core.Node{{ID: "seed", Module: "text", Params: map[string]any{"text": string(seed)}}}
	var edges []core.Edge
	prev := []string{"seed"}
	prevPort := "out"
	for i := 1; i <= hops; i++ {
		hop := []string{fmt.Sprintf("a%02d", i), fmt.Sprintf("b%02d", i)}
		for _, id := range hop {
			nodes = append(nodes, core.Node{ID: id, Module: "merge"})
			for _, src := range prev {
				edges = append(edges, core.Edge{From: src, FromPort: prevPort, To: id, ToPort: "items"})
			}
		}
		prev, prevPort = hop, "out"
	}
	g := graph("mergebomb", nodes, edges)

	if err := core.ValidateRuntime(g, engine.Default.Manifests()); err != nil {
		t.Logf("refused by ValidateRuntime: %v", err)
		return
	}

	runID, err := hs.svc.SubmitGraph(t.Context(), hs.p, g)
	if err != nil {
		t.Logf("refused at submit: %v", err)
		return
	}
	deadline := time.Now().Add(3 * time.Minute)
	var status core.JobStatus = statusHung
	for time.Now().Before(deadline) {
		rec, rerr := hs.jobs.Get(context.Background(), runID)
		if rerr == nil && core.IsTerminalStatus(rec.Status) {
			status = rec.Status
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if status == core.JobStatusSucceeded {
		t.Fatalf("the run SUCCEEDED with the value ceiling at %d bytes and the run-state "+
			"ceiling at %d bytes — neither guard saw the payload",
			core.MaxValueBytes(), core.MaxRunStateBytes())
	}

	// It has to stop for the right reason: a ceiling that fired, not a step
	// that happened to break on the way.
	recs, err := hs.jobs.ListNodeRecords(context.Background(),
		core.ListNodeRecordsOpts{GraphRunID: runID, Limit: 10000})
	if err != nil {
		t.Fatalf("list node records: %v", err)
	}
	var codes []string
	var biggest, biggestMeasured int
	for _, r := range recs {
		if r.Result == nil {
			continue
		}
		if r.Result.Error != nil {
			codes = append(codes, r.Result.Error.Code)
		}
		for _, ref := range r.Result.Output {
			blob, merr := json.Marshal(ref)
			if merr != nil {
				continue
			}
			if len(blob) > biggest {
				biggest = len(blob)
				biggestMeasured = core.ApproxValueSize(ref.Inline, core.MaxValueBytes())
			}
		}
	}
	t.Logf("run %q, stop codes %v; largest stored value %d bytes, measured as %d (limit %d)",
		status, codes, biggest, biggestMeasured, core.MaxValueBytes())

	if !slices.Contains(codes, "value_too_large") && !slices.Contains(codes, "run_state_too_large") {
		t.Errorf("run stopped with codes %v, want value_too_large or run_state_too_large — "+
			"the ceilings must be what refuses the doubling, not an incidental failure", codes)
	}
	// The measurement itself has to be honest, or the ceiling only fires by luck.
	if biggest > 0 && biggestMeasured < biggest/2 {
		t.Errorf("core.ApproxValueSize still under-reports a []core.Ref by %.0f× "+
			"(%d measured vs %d real bytes)",
			float64(biggest)/float64(max(biggestMeasured, 1)), biggestMeasured, biggest)
	}
}
