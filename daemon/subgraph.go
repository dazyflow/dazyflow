package daemon

import (
	"context"
	"encoding/json"
	"fmt"

	"git.sr.ht/~klahr/dazyflow/core"
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

// subgraphDepth counts how many subgraph levels already sit above the
// child about to be submitted, by following ParentNodeRecID links: from
// the parent node-record up through its graph run, that run's parent
// node, and so on. A top-level run (no ParentNodeRecID) ends the walk.
func (s *Service) subgraphDepth(ctx context.Context, parentRec core.JobRecord) int {
	depth := 1 // submitting this child is at least one level deep
	runID := parentRec.GraphRunID
	for i := 0; i < maxSubgraphDepth+2 && runID != ""; i++ {
		run, err := s.Jobs.Get(ctx, runID)
		if err != nil || run.ParentNodeRecID == "" {
			break
		}
		parentNode, err := s.Jobs.Get(ctx, run.ParentNodeRecID)
		if err != nil {
			break
		}
		depth++
		runID = parentNode.GraphRunID
	}
	return depth
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
	if depth := s.subgraphDepth(ctx, parentRec); depth >= maxSubgraphDepth {
		return "", fmt.Errorf("subgraph nesting too deep (%d levels; max %d) — a flow likely references itself", depth, maxSubgraphDepth)
	}

	// System principal scoped to the parent's tenant. Subgraphs may
	// reference private flows — the parent's principal is trusted
	// because they could load and edit the parent in the first
	// place. graph:admin lets this synthetic principal bypass the
	// child's visibility regardless of ownership.
	principal := core.Principal{
		Subject:   "dazyflow-subgraph",
		Tenant:    parentRec.Tenant,
		Workspace: parentRec.Workspace,
		Roles: []core.Role{{
			Name:        "subgraph",
			Permissions: []core.Permission{core.PermGraphRun, core.PermGraphAdmin},
		}},
	}
	return s.submitGraphWithParent(ctx, principal, g, seeds, parentRec.ID)
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
) (string, error) {
	if err := core.AuthorizeGraphRun(p, g); err != nil {
		return "", err
	}
	if err := core.Validate(g); err != nil {
		return "", fmt.Errorf("invalid graph: %w", err)
	}
	if err := validateLoopBodies(g); err != nil {
		return "", fmt.Errorf("invalid graph: %w", err)
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
