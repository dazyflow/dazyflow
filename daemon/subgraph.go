// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/dazyflow/dazyflow/core"
)

// SubGraphRunner is the hook the Worker uses to submit a child graph
// after a subgraph module returns its awaiting result. Service satisfies
// it; tests can stub it.
type SubGraphRunner interface {
	SubmitChild(ctx context.Context, parentRec core.JobRecord, graphID string, seeds map[string]core.Result) (string, error)
}

// maxSubgraphDepth caps how deeply subgraphs may nest. Reaching it almost
// always means a flow references itself (directly or via a cycle); the cap
// turns runaway recursion into a clean per-node error instead of resource
// exhaustion.
const maxSubgraphDepth = 8

// subgraphLineage counts how many subgraph levels already sit above the child
// about to be submitted, AND returns the ID of the top-level run at the root of
// the tree. It follows ParentNodeRecID links: from the parent node-record up
// through its graph run, that run's parent node, and so on. A top-level run (no
// ParentNodeRecID) ends the walk. The root ID is shared by every descendant of
// one trigger, so it keys the per-tree fan-out budget (subtreeBudget).
func (s *Service) subgraphLineage(ctx context.Context, parentRec core.JobRecord) (depth int, root string) {
	depth = 1 // submitting this child is at least one level deep
	root = parentRec.GraphRunID
	runID := parentRec.GraphRunID
	for i := 0; i < maxSubgraphDepth+2 && runID != ""; i++ {
		run, err := s.Jobs.Get(ctx, runID)
		if err != nil {
			break
		}
		root = run.ID // current topmost known ancestor
		if run.ParentNodeRecID == "" {
			break
		}
		parentNode, err := s.Jobs.Get(ctx, run.ParentNodeRecID)
		if err != nil {
			break
		}
		depth++
		runID = parentNode.GraphRunID
	}
	return depth, root
}

// SubmitChild is Service's implementation of SubGraphRunner. The
// principal is synthesized from the parent record's tenant/workspace
// (the system never runs subgraphs as a different identity than the
// parent — keeps audit trails coherent).
func (s *Service) SubmitChild(
	ctx context.Context,
	parentRec core.JobRecord,
	graphID string,
	seeds map[string]core.Result,
) (string, error) {
	store, err := s.Workspaces.Open(parentRec.Tenant, parentRec.Workspace)
	if err != nil {
		return "", fmt.Errorf("open workspace %s/%s: %w", parentRec.Tenant, parentRec.Workspace, err)
	}
	g, err := store.Load(graphID)
	if err != nil {
		return "", fmt.Errorf("load child graph %q: %w", graphID, err)
	}
	if g.Tenant == "" {
		g.Tenant = parentRec.Tenant
	}
	if g.Workspace == "" {
		g.Workspace = parentRec.Workspace
	}

	// Guard against unbounded subgraph recursion: a flow that
	// (transitively) references itself would otherwise spawn children
	// forever. Walk the parent chain and refuse once nesting hits the cap.
	depth, root := s.subgraphLineage(ctx, parentRec)
	if depth >= maxSubgraphDepth {
		return "", fmt.Errorf("subgraph nesting too deep (%d levels; max %d) — a flow likely references itself", depth, maxSubgraphDepth)
	}
	// Depth alone doesn't bound BREADTH: many subgraph nodes per graph fan out
	// to ~N^depth runs. Charge this child against the root tree's total budget
	// and refuse an exponential blow-up before any state is written.
	if !s.subtreeBudgetInst().charge(root) {
		return "", fmt.Errorf("subgraph fan-out limit reached (max %d descendant runs from one trigger) — a flow is spawning too many sub-runs", maxSubgraphRunsPerRoot)
	}

	// System principal scoped to the parent's tenant. Subgraphs may
	// reference private flows — the parent's principal is trusted
	// because they could load and edit the parent in the first
	// place. graph:admin lets this synthetic principal bypass the
	// child's visibility regardless of ownership.
	principal := SystemPrincipal("dazyflow-subgraph", parentRec.Tenant, parentRec.Workspace)
	return s.submitGraphWithParent(ctx, principal, g, seeds, parentRec.ID, s.runTriggerDepth(ctx, parentRec.GraphRunID))
}

// runTriggerDepth is the trigger-chain depth stamped on a graph run, 0 when
// the record can't be read. A child must inherit it: the depth is what the
// HTTP drop stamps on a call to one of our own trigger URLs, so a child that
// started from zero would let `A → subgraph B → HTTP-trigger A` cycle forever
// — each hop resetting the counter that is supposed to stop it, and each
// webhook run being a fresh top-level tree the subgraph lineage walk and the
// fan-out budget can't connect to the last one.
func (s *Service) runTriggerDepth(ctx context.Context, graphRunID string) int {
	if graphRunID == "" {
		return 0
	}
	run, err := s.Jobs.Get(ctx, graphRunID)
	if err != nil {
		return 0
	}
	return run.TriggerDepth
}

// submitGraphWithParent mirrors SubmitGraphWithSeed but stamps the
// child's graph-record with ParentNodeRecID. Kept private because the
// public surface stays SubmitGraph / SubmitGraphWithSeed — sub-graphs
// always flow through SubmitChild.
func (s *Service) submitGraphWithParent(
	ctx context.Context,
	p core.Principal,
	g core.Graph,
	seeds map[string]core.Result,
	parentNodeRecID string,
	triggerDepth int,
) (string, error) {
	if err := core.AuthorizeGraphRun(p, g); err != nil {
		return "", err
	}
	// Counting nodes and wires is O(1) per element and validating is not, so
	// the size ceilings come first: an oversized graph is refused without
	// being walked at all.
	// Resource-exhaustion guard, mirroring SubmitGraphWithSeed: a child graph
	// is no less able to exhaust the daemon than a top-level one.
	if maxNodes := s.effectiveLimits(ctx, g.Tenant).MaxGraphNodes; maxNodes > 0 && len(g.Nodes) > maxNodes {
		return "", fmt.Errorf("%w: graph has %d nodes, limit is %d",
			core.ErrGraphTooLarge, len(g.Nodes), maxNodes)
	}
	if s.MaxGraphEdges > 0 && len(g.Edges) > s.MaxGraphEdges {
		return "", fmt.Errorf("%w: graph has %d connections, limit is %d",
			core.ErrGraphTooLarge, len(g.Edges), s.MaxGraphEdges)
	}
	if err := core.ValidateRuntime(g, s.manifestsSnapshot(g.Tenant)); err != nil {
		return "", fmt.Errorf("invalid graph: %w", err)
	}
	if err := validateLoopBodies(g); err != nil {
		return "", fmt.Errorf("invalid graph: %w", err)
	}
	// Killswitch parity: orgSuspended is the authoritative halt for every run
	// entry point. Unlike the billing quota (which children intentionally
	// bypass so a mid-run flow isn't stranded), a SUSPENDED org must stop
	// spawning work immediately — otherwise an in-flight subgraph tree keeps
	// expanding after an operator pulls the plug.
	if s.orgSuspended(ctx, g.Tenant) {
		return "", core.ErrOrgSuspended
	}
	for nodeID := range seeds {
		if _, ok := g.Node(nodeID); !ok {
			return "", fmt.Errorf("seed targets node %q which is not in graph", nodeID)
		}
	}

	graphRunID, err := newID()
	if err != nil {
		return "", fmt.Errorf("generate run id: %w", err)
	}
	payload, err := json.Marshal(g)
	if err != nil {
		return "", fmt.Errorf("marshal graph: %w", err)
	}
	graphRec := core.JobRecord{
		ID:              graphRunID,
		Kind:            core.JobKindGraph,
		GraphID:         g.ID,
		NodeID:          "*",
		Tenant:          g.Tenant,
		Workspace:       g.Workspace,
		Status:          core.JobStatusRunning,
		GraphPayload:    payload,
		Job:             core.Job{ID: graphRunID, GraphID: g.ID},
		ParentNodeRecID: parentNodeRecID,
		TriggerDepth:    triggerDepth,
	}
	if err := s.Jobs.Enqueue(ctx, graphRec); err != nil {
		return "", fmt.Errorf("enqueue child graph: %w", err)
	}
	if errs := populateSeededRun(ctx, s.Jobs, g, graphRunID, seeds); len(errs) > 0 {
		return graphRunID, fmt.Errorf("enqueue child roots: %v", errs)
	}
	if allNodesAccountedFor(ctx, s.Jobs, g, graphRunID) {
		final := &core.Result{Status: core.StatusOK}
		_ = s.Jobs.Complete(ctx, graphRunID, core.JobStatusSucceeded, final)
		s.bus().Publish(graphRunID, BusEvent{Terminal: &TerminalEvent{
			JobID: graphRunID, Status: core.JobStatusSucceeded,
		}})
	}
	return graphRunID, nil
}
