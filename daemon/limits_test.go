// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/daemon"
)

func TestSubmitGraph_RejectsTooManyNodes(t *testing.T) {
	t.Parallel()
	h := newVisibilityHarness(t)
	h.svc.MaxGraphNodes = 2
	ctx := context.Background()

	tooBig := core.Graph{
		ID: "big", Tenant: "t", Workspace: "ws", Visibility: core.VisibilityOrg,
		Nodes: []core.Node{
			{ID: "a", Module: "noop"},
			{ID: "b", Module: "noop"},
			{ID: "c", Module: "noop"},
		},
	}
	if _, err := h.svc.SubmitGraph(ctx, h.alice, tooBig); !errors.Is(err, core.ErrGraphTooLarge) {
		t.Fatalf("3-node submit under limit 2: err = %v, want ErrGraphTooLarge", err)
	}

	// At the limit, the node-count guard must not be what stops it.
	atLimit := core.Graph{
		ID: "ok", Tenant: "t", Workspace: "ws", Visibility: core.VisibilityOrg,
		Nodes: []core.Node{
			{ID: "a", Module: "noop"},
			{ID: "b", Module: "noop"},
		},
	}
	if _, err := h.svc.SubmitGraph(ctx, h.alice, atLimit); errors.Is(err, core.ErrGraphTooLarge) {
		t.Fatalf("2-node submit at limit 2 rejected as too large: %v", err)
	}
}

// A node ceiling alone does not bound the work: readiness is re-evaluated
// per dependent per completion, so cost scales with WIRES, and a graph well
// inside the node limit can carry hundreds of thousands of them.
func TestSubmitGraph_RejectsTooManyEdges(t *testing.T) {
	t.Parallel()
	h := newVisibilityHarness(t)
	h.svc.MaxGraphEdges = 3
	ctx := context.Background()

	// One distinct wire per pair: identical wires are refused as duplicates
	// before the count cap is reached, and the count cap is what's under test.
	g := func(n int) core.Graph {
		out := core.Graph{
			ID: "wires", Tenant: "t", Workspace: "ws", Visibility: core.VisibilityOrg,
		}
		for i := 0; i < n; i++ {
			from, to := fmt.Sprintf("a%d", i), fmt.Sprintf("b%d", i)
			out.Nodes = append(out.Nodes,
				core.Node{ID: from, Module: "noop"}, core.Node{ID: to, Module: "noop"})
			out.Edges = append(out.Edges, core.Edge{
				From: from, FromPort: "pass", To: to, ToPort: "pass",
			})
		}
		return out
	}

	if _, err := h.svc.SubmitGraph(ctx, h.alice, g(4)); !errors.Is(err, core.ErrGraphTooLarge) {
		t.Errorf("4-wire submit under limit 3: err = %v, want ErrGraphTooLarge", err)
	}
	if _, err := h.svc.SaveGraph(ctx, h.alice, g(4)); !errors.Is(err, core.ErrGraphTooLarge) {
		t.Errorf("4-wire save under limit 3: err = %v, want ErrGraphTooLarge", err)
	}
	if _, err := h.svc.SubmitGraph(ctx, h.alice, g(3)); errors.Is(err, core.ErrGraphTooLarge) {
		t.Errorf("3-wire submit at limit 3 rejected as too large: %v", err)
	}
}

// A flow whose HTTP step calls its own trigger URL produces a chain of
// TOP-LEVEL runs, which the subgraph depth cap and fan-out budget cannot
// see (they walk parent links inside one run tree). The depth the trigger
// endpoint carries is what breaks it.
func TestSubmitGraphOpts_RefusesDeepTriggerChain(t *testing.T) {
	t.Parallel()
	h := newVisibilityHarness(t)
	ctx := context.Background()
	g := core.Graph{
		ID: "chained", Tenant: "t", Workspace: "ws", Visibility: core.VisibilityOrg,
		Nodes: []core.Node{{ID: "a", Module: "noop"}},
	}

	if _, err := h.svc.SubmitGraphOpts(ctx, h.alice, g, daemon.SubmitOpts{
		TriggerDepth: core.MaxTriggerChainDepth - 1,
	}); errors.Is(err, core.ErrTriggerLoop) {
		t.Errorf("a chain one short of the cap was refused: %v", err)
	}
	if _, err := h.svc.SubmitGraphOpts(ctx, h.alice, g, daemon.SubmitOpts{
		TriggerDepth: core.MaxTriggerChainDepth,
	}); !errors.Is(err, core.ErrTriggerLoop) {
		t.Errorf("a chain at the cap: err = %v, want ErrTriggerLoop", err)
	}
}

// A subgraph child must carry its parent's trigger-chain depth. The depth is
// what a step calling one of our own trigger URLs stamps on the request, so a
// child starting from zero let `A --subgraph--> B --HTTP--> A` cycle forever:
// every hop reset the counter, and each webhook run is a fresh top-level tree
// that neither the subgraph lineage walk nor the fan-out budget can connect to
// the last one.
func TestSubmitChild_InheritsTriggerDepth(t *testing.T) {
	t.Parallel()
	h := newVisibilityHarness(t)
	ctx := context.Background()

	child := core.Graph{
		ID: "child", Tenant: "t", Workspace: "ws", Visibility: core.VisibilityOrg,
		Nodes: []core.Node{{ID: "n", Module: "noop"}},
	}
	if _, err := h.svc.SaveGraph(ctx, h.alice, child); err != nil {
		t.Fatalf("save child: %v", err)
	}

	const depth = core.MaxTriggerChainDepth - 1
	parentRun, err := h.svc.SubmitGraphOpts(ctx, h.alice, core.Graph{
		ID: "parent", Tenant: "t", Workspace: "ws", Visibility: core.VisibilityOrg,
		Nodes: []core.Node{{ID: "a", Module: "noop"}},
	}, daemon.SubmitOpts{TriggerDepth: depth})
	if err != nil {
		t.Fatalf("submit parent: %v", err)
	}

	parentNode := core.JobRecord{
		ID: "parent-node", Kind: core.JobKindNode, GraphRunID: parentRun,
		GraphID: "parent", NodeID: "a", Tenant: "t", Workspace: "ws",
	}
	if err := h.svc.Jobs.Enqueue(ctx, parentNode); err != nil {
		t.Fatalf("enqueue parent node: %v", err)
	}

	childRun, err := h.svc.SubmitChild(ctx, parentNode, "child", nil)
	if err != nil {
		t.Fatalf("submit child: %v", err)
	}
	rec, err := h.svc.Jobs.Get(ctx, childRun)
	if err != nil {
		t.Fatalf("get child run: %v", err)
	}
	if rec.TriggerDepth != depth {
		t.Errorf("child run trigger_depth = %d, want %d inherited from the parent",
			rec.TriggerDepth, depth)
	}
}
