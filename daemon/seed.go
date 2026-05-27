package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"git.sr.ht/~klahr/hazy-flow/core"
)

// enqueueReadyDependents looks at every node downstream of completedNodeID
// and, for each one whose predecessors are all already succeeded, writes
// a queued node-record. Used by SubmitGraphWithSeed to kick off graph
// execution when some root nodes start out pre-completed (e.g. a
// webhook trigger seeded webhook_input).
//
// This is a deliberately simple version of the worker's full dispatch
// logic — it doesn't cascade skips or handle fallback edges. Those
// scenarios are correctly handled later when a worker completes a
// regular node and triggers the engine's full dispatcher.
func enqueueReadyDependents(ctx context.Context, store core.JobStore, graph core.Graph, graphRunID, sourceNodeID string) {
	dependents := map[string]struct{}{}
	for _, e := range graph.Edges {
		if e.From == sourceNodeID {
			dependents[e.To] = struct{}{}
		}
	}
	for nodeID := range dependents {
		if !allPredsSucceeded(ctx, store, graph, graphRunID, nodeID) {
			continue
		}
		rec := core.JobRecord{
			ID:         NodeJobID(graphRunID, nodeID),
			Kind:       core.JobKindNode,
			GraphRunID: graphRunID,
			GraphID:    graph.ID,
			NodeID:     nodeID,
			Tenant:     graph.Tenant,
			Workspace:  graph.Workspace,
			Job:        core.Job{GraphID: graph.ID, NodeID: nodeID},
		}
		// Idempotent: conflict means another path enqueued this node
		// first, which is fine.
		_ = store.Enqueue(ctx, rec)
	}
}

func allPredsSucceeded(ctx context.Context, store core.JobStore, graph core.Graph, graphRunID, nodeID string) bool {
	for _, e := range graph.Edges {
		if e.To != nodeID {
			continue
		}
		pred, err := store.Get(ctx, NodeJobID(graphRunID, e.From))
		if err != nil || pred.Status != core.JobStatusSucceeded {
			return false
		}
	}
	return true
}

// SubmitGraphWithSeed is the trigger-fed variant of SubmitGraph. The
// seeds map specifies nodes to pre-complete (status=succeeded with the
// supplied result) — used by webhook triggers to deliver the request
// body to webhook_input nodes.
//
// After creating the seed records, the helper enqueues their immediate
// dependents whose predecessors are now all done. Normal worker
// dispatch takes over from there.
//
// If a seed targets a node that doesn't exist in the graph, the call
// fails before any state is written.
func (s *Service) SubmitGraphWithSeed(
	ctx context.Context,
	p core.Principal,
	g core.Graph,
	seeds map[string]core.Result,
) (string, error) {
	if err := core.AuthorizeGraphRun(p, g); err != nil {
		return "", err
	}
	if err := core.Validate(g); err != nil {
		return "", fmt.Errorf("invalid graph: %w", err)
	}
	// Resource-exhaustion guard: refuse a graph whose node count exceeds
	// the operator's ceiling before allocating any run state.
	if s.MaxGraphNodes > 0 && len(g.Nodes) > s.MaxGraphNodes {
		return "", fmt.Errorf("%w: graph has %d nodes, limit is %d",
			core.ErrGraphTooLarge, len(g.Nodes), s.MaxGraphNodes)
	}
	// Validate seed targets exist in the graph before any state writes.
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
		ID:           graphRunID,
		Kind:         core.JobKindGraph,
		GraphID:      g.ID,
		NodeID:       "*",
		Tenant:       g.Tenant,
		Workspace:    g.Workspace,
		Status:       core.JobStatusRunning,
		GraphPayload: payload,
		Job:          core.Job{ID: graphRunID, GraphID: g.ID},
	}
	if err := s.Jobs.Enqueue(ctx, graphRec); err != nil {
		return "", fmt.Errorf("enqueue graph: %w", err)
	}

	if len(g.Nodes) == 0 {
		_ = s.Jobs.Complete(ctx, graphRunID, core.JobStatusSucceeded, &core.Result{Status: core.StatusOK})
		s.bus().Publish(graphRunID, BusEvent{Terminal: &TerminalEvent{
			JobID: graphRunID, Status: core.JobStatusSucceeded,
		}})
		return graphRunID, nil
	}

	enqueueErrs := populateSeededRun(ctx, s.Jobs, g, graphRunID, seeds)
	if len(enqueueErrs) > 0 {
		merged := errors.Join(enqueueErrs...)
		_ = s.Jobs.Complete(ctx, graphRunID, core.JobStatusFailed, &core.Result{
			Status: core.StatusError,
			Error:  &core.JobError{Code: "enqueue_failed", Message: merged.Error()},
		})
		s.bus().Publish(graphRunID, BusEvent{Terminal: &TerminalEvent{
			JobID:  graphRunID,
			Status: core.JobStatusFailed,
			Error:  &core.JobError{Code: "enqueue_failed", Message: merged.Error()},
		}})
		return graphRunID, fmt.Errorf("enqueue roots: %w", merged)
	}

	// If the graph has no work left for workers (every node is already
	// in a terminal record — happens when seeds cover the whole graph),
	// finalize the graph-record now. Otherwise let workers drive the
	// usual maybeCompleteGraph path.
	if allNodesAccountedFor(ctx, s.Jobs, g, graphRunID) {
		final := &core.Result{Status: core.StatusOK}
		if cerr := s.Jobs.Complete(ctx, graphRunID, core.JobStatusSucceeded, final); cerr == nil {
			s.bus().Publish(graphRunID, BusEvent{Terminal: &TerminalEvent{
				JobID:  graphRunID,
				Status: core.JobStatusSucceeded,
			}})
		}
		return graphRunID, nil
	}

	// Arm the wall-time watchdog. The goroutine subscribes to the bus
	// inside itself so a terminal event from the dispatcher exits it
	// early; the timer is the safety net when nothing completes in time.
	s.startGraphTimeoutWatchdog(graphRunID, g.Tenant, g.Workspace, s.effectiveGraphTimeout(g))

	// Arm the per-graph failure notifier. Same per-run pattern as the
	// timeout watchdog — subscribes to the bus, exits on the first
	// terminal event (firing the notification if status=failed).
	// No-op when the graph has no FailureNotify configured.
	s.startFailureNotifier(g, graphRunID)
	return graphRunID, nil
}

// populateSeededRun writes pre-completed records for every seeded node,
// enqueues every non-seeded root, then kicks dependents whose
// predecessors are already all done. Returns the list of enqueue
// errors so the caller can decide to fail the whole submission.
//
// Used by both the webhook path (SubmitGraphWithSeed) and the subgraph
// path (submitGraphWithParent) so they stay behavior-identical.
func populateSeededRun(
	ctx context.Context,
	store core.JobStore,
	g core.Graph,
	graphRunID string,
	seeds map[string]core.Result,
) []error {
	seededSet := make(map[string]struct{}, len(seeds))
	var enqueueErrs []error

	for nodeID, result := range seeds {
		resultCopy := result
		resultCopy.JobID = NodeJobID(graphRunID, nodeID)
		if resultCopy.Status == "" {
			resultCopy.Status = core.StatusOK
		}
		seedRec := core.JobRecord{
			ID:         NodeJobID(graphRunID, nodeID),
			Kind:       core.JobKindNode,
			GraphRunID: graphRunID,
			GraphID:    g.ID,
			NodeID:     nodeID,
			Tenant:     g.Tenant,
			Workspace:  g.Workspace,
			Status:     core.JobStatusSucceeded,
			Result:     &resultCopy,
			Job:        core.Job{GraphID: g.ID, NodeID: nodeID},
		}
		if err := store.Enqueue(ctx, seedRec); err != nil {
			enqueueErrs = append(enqueueErrs, fmt.Errorf("seed %q: %w", nodeID, err))
			continue
		}
		seededSet[nodeID] = struct{}{}
	}

	hasIncoming := make(map[string]bool, len(g.Nodes))
	for _, e := range g.Edges {
		hasIncoming[e.To] = true
	}
	for _, node := range g.Nodes {
		if hasIncoming[node.ID] {
			continue
		}
		if _, isSeed := seededSet[node.ID]; isSeed {
			continue
		}
		nodeRec := core.JobRecord{
			ID:         NodeJobID(graphRunID, node.ID),
			Kind:       core.JobKindNode,
			GraphRunID: graphRunID,
			GraphID:    g.ID,
			NodeID:     node.ID,
			Tenant:     g.Tenant,
			Workspace:  g.Workspace,
			Job:        core.Job{GraphID: g.ID, NodeID: node.ID},
		}
		if err := store.Enqueue(ctx, nodeRec); err != nil {
			enqueueErrs = append(enqueueErrs, fmt.Errorf("root %q: %w", node.ID, err))
		}
	}

	for seedID := range seededSet {
		enqueueReadyDependents(ctx, store, g, graphRunID, seedID)
	}
	return enqueueErrs
}

// allNodesAccountedFor returns true when every node in the graph has
// a node-record AND that record is in a terminal-success state. Used
// to short-circuit graphs whose entire computation was satisfied by
// seeds (e.g. a one-node graph that just receives a webhook).
func allNodesAccountedFor(ctx context.Context, store core.JobStore, g core.Graph, graphRunID string) bool {
	for _, n := range g.Nodes {
		rec, err := store.Get(ctx, NodeJobID(graphRunID, n.ID))
		if err != nil {
			return false
		}
		if rec.Status != core.JobStatusSucceeded {
			return false
		}
	}
	return true
}
