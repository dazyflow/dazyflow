// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package chaos

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
)

// A flow whose every step references its predecessor twice doubles the
// payload per hop: uncapped, ~20 steps turned 1 KiB into gigabytes and the
// process died with `fatal error: out of memory` inside the template
// resolver — a runtime throw no recover catches, so it took every tenant's
// runs with it. core.MaxValueBytes now bounds a single value at both ends
// (template expansion and the node result), so the compounding step fails
// and the run stops there.
//
// DAZYFLOW_CHAOS_OOM=1 runs the full 22-hop bomb against the real default
// ceiling instead of a small test-set one.
func TestOOM_DoublingTemplateBomb(t *testing.T) {
	hops := 15 // 1 KiB → 32 MiB against a 1 MiB test ceiling
	if os.Getenv("DAZYFLOW_CHAOS_OOM") == "" {
		defer core.SetMaxValueBytes(1 << 20)()
	} else {
		hops = 22 // 1 KiB → 2 GiB against the shipped ceiling
	}
	limit := core.MaxValueBytes()
	defer core.SetMaxRunStateBytes(0)() // isolate the per-value ceiling

	hs := newHarness(t)
	seed := make([]byte, 1024)
	for i := range seed {
		seed[i] = 'x'
	}
	// Each hop is a render_text whose `prefix` names its predecessor's output
	// twice; `rows` comes from one shared constant, and the pass pin orders
	// the chain. Legal wiring throughout — the compounding is the point.
	nodes := []core.Node{
		{ID: "rows_src", Module: "text", Params: map[string]any{"text": `[{"a":1}]`}},
		{ID: "rows", Module: "parse_json"},
		{ID: "n00", Module: "text", Params: map[string]any{"text": string(seed)}},
	}
	edges := []core.Edge{{From: "rows_src", FromPort: "out", To: "rows", ToPort: "in"}}
	for i := 1; i <= hops; i++ {
		prev, id := fmt.Sprintf("n%02d", i-1), fmt.Sprintf("n%02d", i)
		prevPort := "text"
		if i == 1 {
			prevPort = "out" // n00 is the text constant
		}
		nodes = append(nodes, core.Node{ID: id, Module: "render_text", Params: map[string]any{
			"template": `""`,
			"prefix":   fmt.Sprintf("${upstream.%s.%s}${upstream.%s.%s}", prev, prevPort, prev, prevPort),
		}})
		edges = append(edges,
			core.Edge{From: "rows", FromPort: "rows", To: id, ToPort: "rows"},
			core.Edge{From: prev, FromPort: prevPort, To: id, ToPort: core.PassPort},
		)
	}
	status, err := hs.submit(graph("doubler", nodes, edges), 300*time.Second)
	if status != core.JobStatusFailed {
		t.Errorf("doubling flow ended %q (err=%v), want failed at the value ceiling", status, err)
	}

	// Every stored value must be inside the ceiling, and the step that
	// crossed it must say so.
	var stopped string
	recs, _ := hs.jobs.ListByGraph(context.Background(), "doubler")
	for _, r := range recs {
		if r.Result == nil {
			continue
		}
		for port, ref := range r.Result.Output {
			if size, too := core.RefTooLarge(ref); too {
				t.Errorf("node %q stored %d bytes on port %q, over the %d-byte ceiling", r.NodeID, size, port, limit)
			}
		}
		if r.Result.Error != nil && r.Result.Error.Code == "value_too_large" {
			stopped = r.NodeID
			t.Logf("stopped at %s: %s", r.NodeID, r.Result.Error.Message)
		}
	}
	if stopped == "" {
		t.Errorf("no step reported value_too_large — the ceiling never engaged")
	}
}

// Dispatch cost used to be ~O(n^3.2) in node count: 50 nodes 0.2 s, 100 →
// 1.9 s, 200 → 17 s, 400 → 2 m 43 s, for steps that do no work — every
// dependent re-scanned the whole edge list and re-read a record per edge
// after every completion, and each node execution re-parsed the graph
// payload. With the per-run topology index, the per-pass record cache and
// the worker's graph cache that is ~30× cheaper, and the edge ceiling puts
// the pathological shapes out of reach entirely.
func TestDenseGraph_DispatchStaysUsable(t *testing.T) {
	for _, n := range []int{50, 100, 200, 400} {
		t.Run(fmt.Sprintf("%d-nodes", n), func(t *testing.T) {
			hs := newHarness(t)
			nodes := make([]core.Node, n)
			var edges []core.Edge
			for i := 0; i < n; i++ {
				nodes[i] = core.Node{ID: fmt.Sprintf("n%03d", i), Module: "delay", Params: map[string]any{"ms": 0}}
				for j := 0; j < i; j++ {
					edges = append(edges, core.Edge{
						From: fmt.Sprintf("n%03d", j), FromPort: "pass",
						To: fmt.Sprintf("n%03d", i), ToPort: "pass",
					})
				}
			}
			start := time.Now()
			status, err := hs.submit(graph("dense", nodes, edges), 10*time.Minute)
			elapsed := time.Since(start)
			if errors.Is(err, core.ErrGraphTooLarge) {
				t.Logf("nodes=%d edges=%d refused by the edge ceiling: %v", n, len(edges), err)
				return
			}
			t.Logf("nodes=%d edges=%d status=%q err=%v elapsed=%s", n, len(edges), status, err, elapsed.Round(time.Millisecond))
			if status == statusHung {
				t.Fatalf("run never terminated")
			}
			// A flow of no-op steps should not cost seconds per hundred nodes.
			if budget := time.Duration(n) * 10 * time.Millisecond; elapsed > budget {
				t.Errorf("%d no-op nodes took %s (budget %s)", n, elapsed.Round(time.Millisecond), budget)
			}
		})
	}
}

// Every step stores its own copy of what it emitted and the pass pin
// threads the payload through the chain, so run state is payload × steps:
// 4 MiB through 50 steps wrote 209 MiB, and ~4 GiB per run at the node
// ceiling with nothing to stop it. core.MaxRunStateBytes now bounds the run
// as a whole — the step that crosses it fails and the run ends there.
func TestPassPin_StateAmplification(t *testing.T) {
	const payloadMiB, hops = 4, 50
	defer core.SetMaxRunStateBytes(64 << 20)() // 16 hops' worth, not 50
	hs := newHarness(t)
	big := make([]byte, payloadMiB<<20)
	for i := range big {
		big[i] = 'a'
	}
	nodes := []core.Node{{ID: "src", Module: "text", Params: map[string]any{"text": string(big)}}}
	var edges []core.Edge
	prev, prevPort := "src", "out"
	for i := 0; i < hops; i++ {
		id := fmt.Sprintf("hop%03d", i)
		nodes = append(nodes, core.Node{ID: id, Module: "delay", Params: map[string]any{"ms": 0}})
		edges = append(edges, core.Edge{From: prev, FromPort: prevPort, To: id, ToPort: "pass"})
		prev, prevPort = id, "pass"
	}
	status, err := hs.submit(graph("amp", nodes, edges), 2*time.Minute)
	stored := 0
	var stoppedAt string
	recs, _ := hs.jobs.ListByGraph(context.Background(), "amp")
	for _, r := range recs {
		b, _ := json.Marshal(r)
		stored += len(b)
		if r.Result != nil && r.Result.Error != nil && r.Result.Error.Code == "run_state_too_large" {
			stoppedAt = r.NodeID
			t.Logf("stopped at %s: %s", r.NodeID, r.Result.Error.Message)
		}
	}
	t.Logf("payload=%dMiB hops=%d status=%q err=%v stored=%dMiB", payloadMiB, hops, status, err, stored>>20)
	if stoppedAt == "" {
		t.Errorf("the run stored %d MiB without the per-run ceiling engaging", stored>>20)
	}
	if status != core.JobStatusFailed {
		t.Errorf("status=%q, want failed at the run-state ceiling", status)
	}
	// Some overshoot is expected (the crossing step's own result is stored,
	// and a couple of siblings may already be in flight), but not 50 hops of it.
	if limit := core.MaxRunStateBytes(); stored > 4*limit {
		t.Errorf("stored %d MiB against a %d MiB ceiling", stored>>20, limit>>20)
	}
}

// An edge bomb — 500k wires between two steps — is cheap to submit and
// nothing in the data model bounds it, so it has to be refused at the gate.
func TestEdgeBomb_IsRejected(t *testing.T) {
	hs := newHarness(t)
	edges := make([]core.Edge, 500_000)
	for i := range edges {
		edges[i] = core.Edge{From: "a", FromPort: "pass", To: "b", ToPort: "items"}
	}
	g := graph("edgebomb", []core.Node{
		{ID: "a", Module: "delay", Params: map[string]any{"ms": 0}},
		{ID: "b", Module: "merge"},
	}, edges)
	_, saveErr := hs.svc.SaveGraph(t.Context(), hs.p, g)
	status, submitErr := hs.submit(g, 2*time.Minute)
	t.Logf("edges=%d save=%v submit=%v status=%q", len(edges), saveErr, submitErr, status)
	if !errors.Is(saveErr, core.ErrGraphTooLarge) {
		t.Errorf("SaveGraph accepted 500k edges: %v", saveErr)
	}
	if !errors.Is(submitErr, core.ErrGraphTooLarge) {
		t.Errorf("SubmitGraph accepted 500k edges: %v", submitErr)
	}
}
