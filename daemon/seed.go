// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/dazyflow/dazyflow/core"
)

func enqueueReadyDependents(ctx context.Context, store core.JobStore, graph core.Graph, graphRunID, sourceNodeID string) int {
	bodyOwners := loopBodyOwners(graph)
	dependents := map[string]struct{}{}
	for _, e := range graph.Edges {
		if e.From == sourceNodeID {
			dependents[e.To] = struct{}{}
		}
	}
	var ready []core.JobRecord
	for nodeID := range dependents {
		if _, owned := bodyOwners[nodeID]; owned {
			continue
		}
		if !allPredsSucceeded(ctx, store, graph, graphRunID, nodeID) {
			continue
		}
		ready = append(ready, core.JobRecord{
			ID:         NodeJobID(graphRunID, nodeID),
			Kind:       core.JobKindNode,
			GraphRunID: graphRunID,
			GraphID:    graph.ID,
			NodeID:     nodeID,
			Tenant:     graph.Tenant,
			Workspace:  graph.Workspace,
			Job:        core.Job{GraphID: graph.ID, NodeID: nodeID},
		})
	}
	if n, ok := core.TryEnqueueNodes(ctx, store, ready); ok {
		return n
	}
	var queued int
	for _, rec := range ready {
		if err := store.Enqueue(ctx, rec); err == nil {
			queued++
		}
	}
	return queued
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

func (s *Service) SubmitGraphWithSeed(
	ctx context.Context,
	p core.Principal,
	g core.Graph,
	seeds map[string]core.Result,
) (string, error) {
	return s.SubmitGraphOpts(ctx, p, g, SubmitOpts{Seeds: seeds})
}

// What a submission needs beyond the graph.
type SubmitOpts struct {
	Seeds        map[string]core.Result
	Manual       bool
	TriggerDepth int
}

// A run whose creation half-succeeded must reach a terminal state, or it is
// immortal: it holds a concurrency slot for ever and nothing notifies.
func (s *Service) failSubmission(ctx context.Context, graphRunID string, g core.Graph, cause error, publish bool) {
	jobErr := &core.JobError{Code: "enqueue_failed", Message: cause.Error()}
	if err := s.Jobs.Complete(ctx, graphRunID, core.JobStatusFailed, &core.Result{
		Status: core.StatusError,
		Error:  jobErr,
	}); err != nil && s.Logger != nil {
		s.Logger.Printf("submission [%s/%s/%s]: run %s could not be marked failed (%v) after: %v",
			g.Tenant, g.Workspace, g.ID, graphRunID, err, cause)
	}
	if publish {
		s.bus().Publish(graphRunID, BusEvent{Terminal: &TerminalEvent{
			JobID:  graphRunID,
			Status: core.JobStatusFailed,
			Error:  jobErr,
		}})
	}
}

func (s *Service) SubmitGraphOpts(
	ctx context.Context,
	p core.Principal,
	g core.Graph,
	opts SubmitOpts,
) (string, error) {
	seeds := opts.Seeds
	if err := core.AuthorizeGraphRun(p, g); err != nil {
		return "", err
	}
	// Cheap ceilings before the expensive validation.
	if maxNodes := s.effectiveLimits(ctx, g.Tenant).MaxGraphNodes; maxNodes > 0 && len(g.Nodes) > maxNodes {
		return "", fmt.Errorf("%w: graph has %d nodes, limit is %d",
			core.ErrGraphTooLarge, len(g.Nodes), maxNodes)
	}
	if s.MaxGraphEdges > 0 && len(g.Edges) > s.MaxGraphEdges {
		return "", fmt.Errorf("%w: graph has %d connections, limit is %d",
			core.ErrGraphTooLarge, len(g.Edges), s.MaxGraphEdges)
	}
	// The run path cannot honour a wiring it cannot represent.
	if err := core.ValidateRuntime(g, s.manifestsSnapshot(g.Tenant)); err != nil {
		return "", fmt.Errorf("invalid graph: %w", err)
	}
	if err := validateLoopBodies(g); err != nil {
		return "", fmt.Errorf("invalid graph: %w", err)
	}
	// Refused at submit, so a trigger is turned away rather than queued.
	if err := s.checkRunQuota(ctx, g.Tenant); err != nil {
		return "", err
	}
	if s.orgSuspended(ctx, g.Tenant) {
		return "", core.ErrOrgSuspended
	}
	// A flow calling its own trigger URL would otherwise run for ever.
	if opts.TriggerDepth >= core.MaxTriggerChainDepth {
		return "", fmt.Errorf("%w: %d runs deep (max %d) — a flow is triggering itself",
			core.ErrTriggerLoop, opts.TriggerDepth, core.MaxTriggerChainDepth)
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

	admit := len(g.Nodes) == 0 || s.admitGraphRun(ctx, g.Tenant)
	initialStatus := core.JobStatusQueued
	if admit {
		initialStatus = core.JobStatusRunning
	}

	// Atomic and JUST before enqueue: a gap between the check and the increment lets
	// two concurrent submissions both pass a cap with one slot left.
	if s.Usage != nil {
		if admitted, rerr := s.reserveRun(ctx, g.Tenant); rerr != nil {
			if s.Logger != nil {
				s.Logger.Printf("usage metering [%s]: reserve run (failing open): %v", g.Tenant, rerr)
			}
		} else if !admitted {
			return "", fmt.Errorf("%w: monthly run limit reached — upgrade to keep your flows running", core.ErrPlanLimit)
		}
	}

	graphRec := core.JobRecord{
		ID:           graphRunID,
		Kind:         core.JobKindGraph,
		GraphID:      g.ID,
		NodeID:       "*",
		Tenant:       g.Tenant,
		Workspace:    g.Workspace,
		Status:       initialStatus,
		GraphPayload: payload,
		Manual:       opts.Manual,
		TriggerDepth: opts.TriggerDepth,
		Job:          core.Job{ID: graphRunID, GraphID: g.ID},
	}
	if err := s.Jobs.Enqueue(ctx, graphRec); err != nil {
		// Already metered above, so a failure here must release the reservation.
		s.releaseRun(ctx, g.Tenant)
		return "", fmt.Errorf("enqueue graph: %w", err)
	}

	if !admit {
		if errs := persistSeedsOnly(ctx, s.Jobs, g, graphRunID, seeds); len(errs) > 0 {
			merged := errors.Join(errs...)
			s.failSubmission(ctx, graphRunID, g, merged, false)
			return graphRunID, fmt.Errorf("persist seeds: %w", merged)
		}
		return graphRunID, nil
	}

	if len(g.Nodes) == 0 {
		_ = s.Jobs.Complete(ctx, graphRunID, core.JobStatusSucceeded, &core.Result{Status: core.StatusOK})
		s.bus().Publish(graphRunID, BusEvent{Terminal: &TerminalEvent{
			JobID: graphRunID, Status: core.JobStatusSucceeded,
		}})
		return graphRunID, nil
	}

	queued, enqueueErrs := populateSeededRun(ctx, s.Jobs, g, graphRunID, seeds)
	if queued > 0 {
		s.Wake.Notify()
	}
	if len(enqueueErrs) > 0 {
		merged := errors.Join(enqueueErrs...)
		s.failSubmission(ctx, graphRunID, g, merged, true)
		return graphRunID, fmt.Errorf("enqueue roots: %w", merged)
	}

	if queued == 0 && allNodesAccountedFor(ctx, s.Jobs, g, graphRunID) {
		final := &core.Result{Status: core.StatusOK}
		if cerr := s.Jobs.Complete(ctx, graphRunID, core.JobStatusSucceeded, final); cerr == nil {
			s.bus().Publish(graphRunID, BusEvent{Terminal: &TerminalEvent{
				JobID:  graphRunID,
				Status: core.JobStatusSucceeded,
			}})
		}
		return graphRunID, nil
	}

	// Subscribes before returning, or a fast run finishes before anyone is watching.
	s.startGraphTimeoutWatchdog(graphRunID, g.Tenant, g.Workspace, s.effectiveGraphTimeout(g))

	return graphRunID, nil
}

func persistSeedsOnly(
	ctx context.Context,
	store core.JobStore,
	g core.Graph,
	graphRunID string,
	seeds map[string]core.Result,
) []error {
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
		}
	}
	return enqueueErrs
}

// Roots only: everything else is released by its predecessors completing.
func dispatchRoots(
	ctx context.Context,
	store core.JobStore,
	g core.Graph,
	graphRunID string,
	seededNodeIDs map[string]struct{},
) (int, []error) {
	var queued int
	var enqueueErrs []error
	hasIncoming := make(map[string]bool, len(g.Nodes))
	for _, e := range g.Edges {
		hasIncoming[e.To] = true
	}
	var roots []core.JobRecord
	for _, node := range g.Nodes {
		if hasIncoming[node.ID] {
			continue
		}
		if _, isSeed := seededNodeIDs[node.ID]; isSeed {
			continue
		}
		roots = append(roots, core.JobRecord{
			ID:         NodeJobID(graphRunID, node.ID),
			Kind:       core.JobKindNode,
			GraphRunID: graphRunID,
			GraphID:    g.ID,
			NodeID:     node.ID,
			Tenant:     g.Tenant,
			Workspace:  g.Workspace,
			Job:        core.Job{GraphID: g.ID, NodeID: node.ID},
		})
	}
	if n, ok := core.TryEnqueueNodes(ctx, store, roots); ok {
		queued = n
	} else {
		for _, nodeRec := range roots {
			if err := store.Enqueue(ctx, nodeRec); err != nil {
				enqueueErrs = append(enqueueErrs, fmt.Errorf("root %q: %w", nodeRec.NodeID, err))
				continue
			}
			queued++
		}
	}
	for seedID := range seededNodeIDs {
		queued += enqueueReadyDependents(ctx, store, g, graphRunID, seedID)
	}
	return queued, enqueueErrs
}

func populateSeededRun(
	ctx context.Context,
	store core.JobStore,
	g core.Graph,
	graphRunID string,
	seeds map[string]core.Result,
) (int, []error) {
	errs := persistSeedsOnly(ctx, store, g, graphRunID, seeds)
	seededSet := make(map[string]struct{}, len(seeds))
	for nodeID := range seeds {
		seededSet[nodeID] = struct{}{}
	}
	queued, dispatchErrs := dispatchRoots(ctx, store, g, graphRunID, seededSet)
	return queued, append(errs, dispatchErrs...)
}

func allNodesAccountedFor(ctx context.Context, store core.JobStore, g core.Graph, graphRunID string) bool {
	bodyOwners := loopBodyOwners(g)
	for _, n := range g.Nodes {
		if _, owned := bodyOwners[n.ID]; owned {
			continue
		}
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
