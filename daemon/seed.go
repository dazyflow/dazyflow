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
	// Loop-body nodes run once per item under their for_each, never standalone,
	// so the seed path must skip them just as the live dispatcher does.
	bodyOwners := loopBodyOwners(graph)
	dependents := map[string]struct{}{}
	for _, e := range graph.Edges {
		if e.From == sourceNodeID {
			dependents[e.To] = struct{}{}
		}
	}
	for nodeID := range dependents {
		if _, owned := bodyOwners[nodeID]; owned {
			continue
		}
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
	return s.SubmitGraphOpts(ctx, p, g, SubmitOpts{Seeds: seeds})
}

// SubmitOpts carries what a submission needs beyond the graph itself.
//
// A struct rather than more parameters because the two things in it are set by
// different callers for different reasons, and every automatic trigger — the
// scheduler, webhooks, forms — wants the zero value. Adding them as positional
// arguments would have made eight call sites say "nil, false" to mean
// "ordinary run".
type SubmitOpts struct {
	// Seeds pre-completes nodes: a webhook body, a form submission, the
	// already-succeeded steps of a run being retried.
	Seeds map[string]core.Result
	// Manual marks a run a person started from the app and is watching. See
	// core.JobRecord.Manual for what it changes and why it is persisted.
	Manual bool
	// TriggerDepth is how deep the trigger chain that reached this
	// submission already is — set by the trigger endpoints from the inbound
	// core.TriggerDepthHeader. See core.JobRecord.TriggerDepth.
	TriggerDepth int
}

// SubmitGraphOpts is the one implementation the other two delegate to.
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
	// Same migration the save path applies: a run submitted straight from an
	// older client's payload should not fail on a wire the model no longer has.
	g = core.MigrateGraph(g)
	// Counting nodes and wires is O(1) per element and validating is not, so
	// the size ceilings come first: an oversized graph is refused without
	// being walked at all.
	// Resource-exhaustion guard: refuse a graph whose node count exceeds
	// the tenant's effective ceiling (tier/override, falling back to the
	// global MaxGraphNodes) before allocating any run state.
	if maxNodes := s.effectiveLimits(ctx, g.Tenant).MaxGraphNodes; maxNodes > 0 && len(g.Nodes) > maxNodes {
		return "", fmt.Errorf("%w: graph has %d nodes, limit is %d",
			core.ErrGraphTooLarge, len(g.Nodes), maxNodes)
	}
	if s.MaxGraphEdges > 0 && len(g.Edges) > s.MaxGraphEdges {
		return "", fmt.Errorf("%w: graph has %d connections, limit is %d",
			core.ErrGraphTooLarge, len(g.Edges), s.MaxGraphEdges)
	}
	// The full wiring gate, not just the structural one: the run path cannot
	// honour a wiring the data model can't represent (a second wire into a
	// single-value input silently wins), so a graph that reaches a worker has
	// to have passed the same port rules the editor shows.
	if err := core.ValidateRuntime(g, s.manifestsSnapshot(g.Tenant)); err != nil {
		return "", fmt.Errorf("invalid graph: %w", err)
	}
	if err := validateLoopBodies(g); err != nil {
		return "", fmt.Errorf("invalid graph: %w", err)
	}
	// Plan gate (T3): free-tier tenants get FreeRunsPerMonth runs per
	// calendar month; over the cap the submission is refused with
	// core.ErrPlanLimit (HTTP 402 at the gateway) before any state is
	// written. Applies to every entry point — manual Run, scheduler,
	// webhook/form triggers — since they all pass through here. Nested
	// sub-graph runs bypass it (submitGraphWithParent): their parent
	// was already admitted, and stranding a mid-run graph would be
	// worse than one over-cap child.
	if err := s.checkRunQuota(ctx, g.Tenant); err != nil {
		return "", err
	}
	// Concurrency admission is decided AFTER the record is created (below): a
	// free tenant over its max_concurrency starts the run as pending (queued)
	// rather than running, and the promotion sweep starts it when a slot frees.
	// Platform-admin killswitch: a suspended org runs nothing. This is the
	// authoritative halt — every run entry point (manual, scheduler,
	// webhook/form trigger) funnels through here, including the inbound
	// trigger paths that don't carry a user principal the auth gate could
	// reject. Nested sub-graph runs bypass it for the same reason the plan
	// gate does (the parent was already admitted).
	if s.orgSuspended(ctx, g.Tenant) {
		return "", core.ErrOrgSuspended
	}
	// Trigger-chain breaker: this run was set off by another run's step
	// calling one of our own trigger URLs. Each such run is top-level, so
	// the subgraph depth cap and fan-out budget — which walk parent links
	// within one run tree — cannot see the cycle; refuse past the cap
	// instead, before any state is written.
	if opts.TriggerDepth >= core.MaxTriggerChainDepth {
		return "", fmt.Errorf("%w: %d runs deep (max %d) — a flow is triggering itself",
			core.ErrTriggerLoop, opts.TriggerDepth, core.MaxTriggerChainDepth)
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

	// Concurrency admission: a free tenant already at its max_concurrency
	// running graph runs starts this one PENDING (queued) instead of running;
	// the promotion sweep starts it when a slot frees. An empty graph completes
	// instantly, so always admit it rather than stranding it in the queue.
	// Pro/comped/trial and a 0 limit always admit.
	admit := len(g.Nodes) == 0 || s.admitGraphRun(ctx, g.Tenant)
	initialStatus := core.JobStatusQueued
	if admit {
		initialStatus = core.JobStatusRunning
	}

	// Authoritative run-cap gate + metering, atomic and JUST before enqueue:
	// reserveRun counts one run iff the tenant is under its monthly cap. The
	// earlier checkRunQuota is a fast read-path reject; this closes the
	// check-then-increment race where concurrent submissions at the limit all
	// passed the read. admitted=false → refuse before any state is written; a
	// store error fails open (proceed). This REPLACES the old post-enqueue
	// AddRun — reserveRun already metered the accepted run.
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
		return "", fmt.Errorf("enqueue graph: %w", err)
	}

	// (Run metering happens in reserveRun above, before enqueue, so the cap
	// gate and the count are a single atomic step. Nested sub-graph runs go
	// through submitGraphWithParent and are deliberately NOT counted — their
	// nodes still meter as node executions, and counting the child run too
	// would double-bill one user action.)

	// Pending (admission-deferred) run: persist its seeds now so the promoter
	// can dispatch from them later, but enqueue no runnable work and arm no
	// watchdog until it's promoted to running.
	if !admit {
		if errs := persistSeedsOnly(ctx, s.Jobs, g, graphRunID, seeds); len(errs) > 0 {
			merged := errors.Join(errs...)
			_ = s.Jobs.Complete(ctx, graphRunID, core.JobStatusFailed, &core.Result{
				Status: core.StatusError,
				Error:  &core.JobError{Code: "enqueue_failed", Message: merged.Error()},
			})
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
	s.startFailureNotifier(g, graphRunID, opts.Manual)
	return graphRunID, nil
}

// persistSeedsOnly writes the pre-completed node-records for every seeded node
// and returns the set of node IDs it seeded. It enqueues NO runnable (root)
// work — that's dispatchRoots. The split lets the concurrency admission queue
// persist a pending run's seeds at submit and defer dispatch until promotion.
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

// dispatchRoots enqueues the runnable root node-records (no incoming edge, not
// already seeded) and fans out from each already-seeded node. Call exactly once
// when a run actually starts (immediately for admitted runs, or at promotion
// for a pending one). seededNodeIDs reports which nodes carry pre-completed
// seed records.
func dispatchRoots(
	ctx context.Context,
	store core.JobStore,
	g core.Graph,
	graphRunID string,
	seededNodeIDs map[string]struct{},
) []error {
	var enqueueErrs []error
	hasIncoming := make(map[string]bool, len(g.Nodes))
	for _, e := range g.Edges {
		hasIncoming[e.To] = true
	}
	for _, node := range g.Nodes {
		if hasIncoming[node.ID] {
			continue
		}
		if _, isSeed := seededNodeIDs[node.ID]; isSeed {
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
	for seedID := range seededNodeIDs {
		enqueueReadyDependents(ctx, store, g, graphRunID, seedID)
	}
	return enqueueErrs
}

// populateSeededRun persists seeds and dispatches roots in one step — the
// immediate-start path used by admitted top-level runs and subgraph children.
func populateSeededRun(
	ctx context.Context,
	store core.JobStore,
	g core.Graph,
	graphRunID string,
	seeds map[string]core.Result,
) []error {
	errs := persistSeedsOnly(ctx, store, g, graphRunID, seeds)
	seededSet := make(map[string]struct{}, len(seeds))
	for nodeID := range seeds {
		seededSet[nodeID] = struct{}{}
	}
	return append(errs, dispatchRoots(ctx, store, g, graphRunID, seededSet)...)
}

// allNodesAccountedFor returns true when every node in the graph has
// a node-record AND that record is in a terminal-success state. Used
// to short-circuit graphs whose entire computation was satisfied by
// seeds (e.g. a one-node graph that just receives a webhook).
func allNodesAccountedFor(ctx context.Context, store core.JobStore, g core.Graph, graphRunID string) bool {
	bodyOwners := loopBodyOwners(g)
	for _, n := range g.Nodes {
		// Loop-body nodes never run in the parent run, so they hold no
		// record and must not block the "all accounted for" short-circuit.
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
