package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/engine"
)

// subgraphOutputBinding mirrors the JSON shape the subgraph module
// writes into its awaiting Result. We duplicate the trivial struct here
// rather than importing modules/flow so daemon stays decoupled from any
// one module's package.
type subgraphOutputBinding struct {
	Node string `json:"node"`
	Port string `json:"port"`
}

// Dispatcher advances graph state after a node-record reaches a definite
// outcome. It is shared by the Worker (which calls it after each Execute)
// and by the approval path (which calls it after a human resumes a paused
// node). The dispatcher is purely state-driven — given a graph and a
// completed node ID, it walks dependents, classifies edges, enqueues
// ready nodes, marks the unreachable ones skipped, and finalizes the
// graph-record when nothing remains.
type Dispatcher struct {
	store  core.JobStore
	bus    Bus
	engine *engine.Engine
	logger *log.Logger
}

// NewDispatcher returns a Dispatcher that uses store/bus/engine for I/O.
// logger may be nil — in which case dispatcher writes are discarded.
func NewDispatcher(store core.JobStore, bus Bus, eng *engine.Engine, logger *log.Logger) *Dispatcher {
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	return &Dispatcher{store: store, bus: bus, engine: eng, logger: logger}
}

// reapBatchLimit bounds one reaper sweep. Runs that complete drop out of the
// running filter, so anything beyond the batch is picked up on the next sweep.
const reapBatchLimit = 500

// ReapStuckGraphRuns recovers graph-run records still marked running whose
// every node-record has already reached a terminal state, and finalizes them.
//
// Normally the dispatcher finalizes a graph run as a side effect of the last
// node's terminal transition (maybeCompleteGraph). If a worker dies in the
// window between that node's terminal write and the completion check — or the
// process is killed mid-finalize — the graph record is left running forever,
// with no lease and no node transition left to re-fire it. This sweep closes
// that gap: it re-runs the completion check for each running graph run, which
// is a no-op for runs that genuinely still have work outstanding and a clean
// finalize (success, propagated failure, parent-subgraph resume, scratch
// reclaim — exactly the live path) for runs that are actually done.
//
// Safe to run on every replica concurrently: Complete is terminal-guarded, so
// only one finalize wins and a healthy in-flight run is never disturbed.
// Returns the number of runs finalized this sweep.
func (d *Dispatcher) ReapStuckGraphRuns(ctx context.Context) (int, error) {
	runs, err := d.store.ListGraphRuns(ctx, core.ListGraphRunsOpts{
		Status: core.JobStatusRunning,
		Limit:  reapBatchLimit,
	})
	if err != nil {
		return 0, err
	}
	reaped := 0
	for _, run := range runs {
		if len(run.GraphPayload) == 0 {
			continue
		}
		var g core.Graph
		if err := json.Unmarshal(run.GraphPayload, &g); err != nil {
			d.logger.Printf("reaper: graph run %s has unparseable payload, skipping: %v", run.ID, err)
			continue
		}
		// A non-failing lastStatus + empty lastNodeID skips the fast-path and
		// runs the full all-terminal evaluation; if the run isn't actually
		// done this returns without touching it.
		d.maybeCompleteGraph(ctx, g, run.ID, "", core.JobStatusSucceeded, nil)
		if rec, err := d.store.Get(ctx, run.ID); err == nil && core.IsTerminalStatus(rec.Status) {
			reaped++
			d.logger.Printf("reaper: recovered orphaned graph run %s → %s", run.ID, rec.Status)
		}
	}
	return reaped, nil
}

// AdvanceAfterCompletion is the single entry-point used by the worker
// and the approval handler once a node has reached its final outcome.
// It centralizes the "publish node-status + dispatch dependents +
// check graph completion" sequence so callers don't have to reason
// about failure-propagation rules or remember to fire bus events.
func (d *Dispatcher) AdvanceAfterCompletion(
	ctx context.Context,
	graph core.Graph,
	graphRunID, nodeID string,
	status core.JobStatus,
	resultErr *core.JobError,
) {
	d.PublishNodeStatus(graphRunID, nodeID, status, resultErr)
	// Cancel guard: if Service.CancelGraphRun has already marked the
	// graph-record terminal, the cancel path published its own Terminal
	// event and the user's intent is "no more downstream work." Skip
	// dispatchReady and maybeCompleteGraph so a node that finished mid
	// cancel doesn't enqueue dependents or double-publish completion.
	if grec, err := d.store.Get(ctx, graphRunID); err == nil && core.IsTerminalStatus(grec.Status) {
		return
	}
	// Breakpoint / step gate (#12): if this node carries a breakpoint (or
	// the run is stepping), hold here — don't dispatch its dependents and
	// don't complete the graph. The node keeps its Succeeded status (output
	// inspectable); the run idles until Continue/Step re-drives it via
	// Service.ResumeGraphRun. Only pause on success — a failed breakpoint
	// node should still propagate failure normally.
	if status == core.JobStatusSucceeded && shouldPauseAfter(graph, graphRunID, nodeID) {
		breakpoints.addPaused(graphRunID, nodeID)
		d.bus.Publish(graphRunID, BusEvent{Paused: &PausedEvent{
			NodeID:   nodeID,
			Stepping: breakpoints.isStepping(graphRunID),
		}})
		return
	}
	if status == core.JobStatusSucceeded ||
		(status == core.JobStatusFailed && !d.failurePropagates(graph, nodeID)) {
		d.dispatchReady(ctx, graph, graphRunID, nodeID)
	}
	d.maybeCompleteGraph(ctx, graph, graphRunID, nodeID, status, resultErr)
}

// resumeFrom re-drives advancement from the nodes a run is paused after —
// dispatching their dependents and re-checking graph completion, exactly
// the work AdvanceAfterCompletion skipped at the breakpoint. Shared by the
// Continue and Step paths in Service.ResumeGraphRun.
func (d *Dispatcher) resumeFrom(ctx context.Context, graph core.Graph, graphRunID string, nodeIDs []string) {
	for _, nodeID := range nodeIDs {
		d.dispatchReady(ctx, graph, graphRunID, nodeID)
		d.maybeCompleteGraph(ctx, graph, graphRunID, nodeID, core.JobStatusSucceeded, nil)
	}
}

// PublishNodeStatus emits a NodeStatusEvent for subscribers (the SSE
// stream is the primary consumer). Exported so worker paths that
// don't go through AdvanceAfterCompletion (notably the awaiting park)
// can publish their own status.
func (d *Dispatcher) PublishNodeStatus(
	graphRunID, nodeID string,
	status core.JobStatus,
	resultErr *core.JobError,
) {
	d.bus.Publish(graphRunID, BusEvent{NodeStatus: &NodeStatusEvent{
		NodeID: nodeID,
		Status: status,
		Error:  resultErr,
	}})
}

func (d *Dispatcher) dispatchReady(ctx context.Context, graph core.Graph, graphRunID, completedNodeID string) {
	// Loop-body nodes run once per item under their for_each (see loopBodyOwners),
	// never standalone — so the normal dispatcher must skip them, including the
	// for_each's own "body" pin edge that feeds the body entry node.
	bodyOwners := loopBodyOwners(graph)
	dependents := map[string]struct{}{}
	for _, edge := range graph.Edges {
		if edge.From == completedNodeID {
			dependents[edge.To] = struct{}{}
		}
	}
	for nodeID := range dependents {
		if _, owned := bodyOwners[nodeID]; owned {
			continue
		}
		switch decision, reason := d.analyzeDependent(ctx, graph, graphRunID, nodeID); decision {
		case depEnqueue:
			newRec := core.JobRecord{
				ID:         NodeJobID(graphRunID, nodeID),
				Kind:       core.JobKindNode,
				GraphRunID: graphRunID,
				GraphID:    graph.ID,
				NodeID:     nodeID,
				Tenant:     graph.Tenant,
				Workspace:  graph.Workspace,
				Job:        core.Job{GraphID: graph.ID, NodeID: nodeID},
			}
			if err := d.store.Enqueue(ctx, newRec); err != nil && !errors.Is(err, core.ErrConflict) {
				d.logger.Printf("enqueue dependent %s: %v", nodeID, err)
			}
		case depSkipped:
			d.recordSkipped(ctx, graph, graphRunID, nodeID, reason)
		case depWaiting:
			if reason != "" {
				d.logger.Printf("%s waiting: %s", nodeID, reason)
			}
		}
	}
}

func (d *Dispatcher) recordSkipped(ctx context.Context, graph core.Graph, graphRunID, nodeID, reason string) {
	rec := core.JobRecord{
		ID:         NodeJobID(graphRunID, nodeID),
		Kind:       core.JobKindNode,
		GraphRunID: graphRunID,
		GraphID:    graph.ID,
		NodeID:     nodeID,
		Tenant:     graph.Tenant,
		Workspace:  graph.Workspace,
		Status:     core.JobStatusSkipped,
		Job:        core.Job{GraphID: graph.ID, NodeID: nodeID},
	}
	if err := d.store.Enqueue(ctx, rec); err != nil {
		if !errors.Is(err, core.ErrConflict) {
			d.logger.Printf("record skipped %s: %v", nodeID, err)
		}
		return
	}
	d.logger.Printf("skipped %s: %s", nodeID, reason)
	d.PublishNodeStatus(graphRunID, nodeID, core.JobStatusSkipped, nil)
	d.dispatchReady(ctx, graph, graphRunID, nodeID)
	d.maybeCompleteGraph(ctx, graph, graphRunID, nodeID, core.JobStatusSkipped, nil)
}

type dependentDecision int

const (
	depWaiting dependentDecision = iota
	depEnqueue
	depSkipped
)

func (d *Dispatcher) analyzeDependent(ctx context.Context, graph core.Graph, graphRunID, depID string) (dependentDecision, string) {
	var anyActive, anyBlocked bool
	var firstReason string
	for _, edge := range graph.Edges {
		if edge.To != depID {
			continue
		}
		predRec, err := d.store.Get(ctx, NodeJobID(graphRunID, edge.From))
		if err != nil {
			return depWaiting, fmt.Sprintf("predecessor %q not yet recorded", edge.From)
		}
		if !core.IsTerminalStatus(predRec.Status) {
			return depWaiting, fmt.Sprintf("predecessor %q is %s", edge.From, predRec.Status)
		}
		switch outcome := classifyEdge(predRec, edge); outcome {
		case edgeActive:
			anyActive = true
		case edgeDormant:
			// dormant: doesn't activate, doesn't block
		case edgeBlocking:
			if !anyBlocked {
				firstReason = fmt.Sprintf("predecessor %q is %s via %q edge",
					edge.From, predRec.Status, edge.OnError)
			}
			anyBlocked = true
		}
	}
	if anyBlocked {
		return depSkipped, firstReason
	}
	if !anyActive {
		return depSkipped, "all incoming edges dormant (no output on any FromPort, or fallback edges from succeeded preds)"
	}
	return depEnqueue, ""
}

type edgeOutcome int

const (
	edgeActive   edgeOutcome = iota // contributes to running this dependent
	edgeDormant                     // does not contribute but does not block either
	edgeBlocking                    // would prevent the dependent from running
)

func classifyEdge(predRec core.JobRecord, edge core.Edge) edgeOutcome {
	switch predRec.Status {
	case core.JobStatusSucceeded:
		if edge.OnError == core.OnErrorFallback {
			return edgeDormant
		}
		// The pass pin is a CONTROL pin (Unreal-style exec): wiring it means
		// "run after this step", whether or not a value threaded through the
		// predecessor's pass-in. Without this, a pass→pass sequencing wire
		// from a node with an empty pass-in reads as dormant and silently
		// skips everything downstream. The no-output dormancy below is for
		// DATA routing (e.g. branch emitting only then/else) — control
		// edges activate on success alone.
		if edge.FromPort == core.PassPort {
			return edgeActive
		}
		if predRec.Result == nil || predRec.Result.Output == nil {
			return edgeDormant
		}
		if _, ok := predRec.Result.Output[edge.FromPort]; !ok {
			return edgeDormant
		}
		return edgeActive
	case core.JobStatusFailed:
		switch edge.OnError {
		case core.OnErrorSkip, core.OnErrorFallback:
			return edgeActive
		default:
			return edgeBlocking
		}
	case core.JobStatusSkipped:
		switch edge.OnError {
		case core.OnErrorSkip:
			return edgeActive
		case core.OnErrorFallback:
			return edgeDormant
		default:
			return edgeBlocking
		}
	default:
		return edgeBlocking
	}
}

func (d *Dispatcher) failurePropagates(graph core.Graph, nodeID string) bool {
	var hasOutgoing, hasFallback, hasNonTolerant bool
	for _, edge := range graph.Edges {
		if edge.From != nodeID {
			continue
		}
		hasOutgoing = true
		switch edge.OnError {
		case core.OnErrorFallback:
			hasFallback = true
		case core.OnErrorSkip:
			// tolerated locally
		default:
			hasNonTolerant = true
		}
	}
	if !hasOutgoing {
		return true
	}
	if hasFallback {
		return false
	}
	return hasNonTolerant
}

func (d *Dispatcher) maybeCompleteGraph(
	ctx context.Context,
	graph core.Graph,
	graphRunID, lastNodeID string,
	lastStatus core.JobStatus,
	lastErr *core.JobError,
) {
	if lastStatus == core.JobStatusFailed && d.failurePropagates(graph, lastNodeID) {
		d.markGraphFailed(ctx, graph, graphRunID, lastNodeID, lastErr)
		return
	}

	// One batch read of the run's node records, then check completion
	// against the in-memory map — instead of a point Get per node. That
	// keeps a graph run's completion checking to O(nodes) round trips
	// total rather than O(nodes²) (every node's terminal transition used
	// to re-Get every other node). Still store-backed (not an in-process
	// counter), so it stays correct when sibling nodes complete on other
	// dzd replicas writing the same shared store.
	recs, err := d.store.ListNodeRecords(ctx, core.ListNodeRecordsOpts{
		Tenant:     graph.Tenant,
		Workspace:  graph.Workspace,
		GraphRunID: graphRunID,
		Limit:      len(graph.Nodes) + 1,
	})
	if err != nil {
		return
	}
	byNode := make(map[string]core.JobRecord, len(recs))
	for _, r := range recs {
		byNode[r.NodeID] = r
	}

	// Loop-body nodes never run in the parent run (the for_each executes them
	// once per item), so they hold no record here and must not gate completion.
	bodyOwners := loopBodyOwners(graph)
	nodeResults := make(map[string]core.Result, len(graph.Nodes))
	for _, n := range graph.Nodes {
		if _, owned := bodyOwners[n.ID]; owned {
			continue
		}
		rec, ok := byNode[n.ID]
		// A missing or non-terminal node means the run isn't done yet.
		// Under-fetching here can only err toward "not complete", never
		// toward a false completion — safe.
		if !ok || !core.IsTerminalStatus(rec.Status) {
			return
		}
		if rec.Status == core.JobStatusFailed && d.failurePropagates(graph, n.ID) {
			var perr *core.JobError
			if rec.Result != nil {
				perr = rec.Result.Error
			}
			d.markGraphFailed(ctx, graph, graphRunID, n.ID, perr)
			return
		}
		if rec.Result != nil {
			nodeResults[n.ID] = *rec.Result
		}
	}

	final := &core.Result{Status: core.StatusOK}
	if cerr := d.store.Complete(ctx, graphRunID, core.JobStatusSucceeded, final); cerr == nil {
		breakpoints.clear(graphRunID)
		d.reclaimScratch(graph, graphRunID)
		d.bus.Publish(graphRunID, BusEvent{Terminal: &TerminalEvent{
			JobID:  graphRunID,
			Status: core.JobStatusSucceeded,
			GraphRes: engine.GraphResult{
				GraphID: graph.ID,
				Status:  core.StatusOK,
				Nodes:   nodeResults,
			},
		}})
		d.maybeResumeParent(ctx, graphRunID, core.StatusOK, nil)
	}
}

// reclaimScratch removes a finished run's ephemeral scratch directory
// (everything written under a scratch:// path). Best-effort: a reclaim
// failure is logged, never fatal, so it can't block or fail completion.
// No-op when the sandbox provider doesn't support scratch, or when the
// run never created any (RemoveScratch is idempotent).
func (d *Dispatcher) reclaimScratch(graph core.Graph, graphRunID string) {
	if d.engine == nil {
		return
	}
	sp, ok := d.engine.Sandbox.(core.ScratchProvider)
	if !ok {
		return
	}
	if err := sp.RemoveScratch(graph.Tenant, graph.Workspace, graphRunID); err != nil {
		d.logger.Printf("scratch reclaim for run %s: %v", graphRunID, err)
	}
}

func (d *Dispatcher) markGraphFailed(
	ctx context.Context,
	graph core.Graph,
	graphRunID, blameNode string,
	cause *core.JobError,
) {
	errPayload := cause
	if errPayload == nil {
		errPayload = &core.JobError{
			Code:    "node_failed",
			Message: fmt.Sprintf("node %q failed", blameNode),
		}
	}
	result := &core.Result{Status: core.StatusError, Error: errPayload}
	if cerr := d.store.Complete(ctx, graphRunID, core.JobStatusFailed, result); cerr == nil {
		breakpoints.clear(graphRunID)
		d.reclaimScratch(graph, graphRunID)
		d.bus.Publish(graphRunID, BusEvent{Terminal: &TerminalEvent{
			JobID:  graphRunID,
			Status: core.JobStatusFailed,
			Error:  errPayload,
			GraphRes: engine.GraphResult{
				GraphID: graph.ID,
				Status:  core.StatusError,
				Error:   errPayload,
			},
		}})
		d.maybeResumeParent(ctx, graphRunID, core.StatusError, errPayload)
	}
}

// maybeResumeParent walks the child→parent linkage and, if present,
// transitions the parent's awaiting record to terminal. Outputs are
// computed by reading the parent node's pending_output_map and pulling
// the named (childNode, port) from the child's per-node records.
//
// A child failure propagates to the parent as a node failure with code
// "child_failed" — the parent's graph then applies its usual OnError /
// fallback rules to decide whether to continue or abort.
func (d *Dispatcher) maybeResumeParent(
	ctx context.Context,
	childRunID string,
	childStatus string,
	childErr *core.JobError,
) {
	childGraphRec, err := d.store.Get(ctx, childRunID)
	if err != nil || childGraphRec.ParentNodeRecID == "" {
		return
	}
	parentRec, err := d.store.Get(ctx, childGraphRec.ParentNodeRecID)
	if err != nil {
		d.logger.Printf("parent %s missing: %v", childGraphRec.ParentNodeRecID, err)
		return
	}
	if parentRec.Status != core.JobStatusAwaiting {
		// Likely cancelled or already failed via some other path.
		return
	}

	var (
		parentStatus core.JobStatus
		parentResult *core.Result
	)
	if childStatus == core.StatusError {
		parentStatus = core.JobStatusFailed
		parentResult = &core.Result{
			JobID:  parentRec.ID,
			Status: core.StatusError,
			Error: &core.JobError{
				Code:    "child_failed",
				Message: fmt.Sprintf("child graph %s failed: %s", childRunID, childErrMessage(childErr)),
			},
		}
	} else {
		out, perr := d.projectChildOutputs(ctx, parentRec, childRunID)
		if perr != nil {
			parentStatus = core.JobStatusFailed
			parentResult = &core.Result{
				JobID:  parentRec.ID,
				Status: core.StatusError,
				Error:  &core.JobError{Code: "child_output_map", Message: perr.Error()},
			}
		} else {
			parentStatus = core.JobStatusSucceeded
			parentResult = &core.Result{JobID: parentRec.ID, Status: core.StatusOK, Output: out}
		}
	}

	if cerr := d.store.Complete(ctx, parentRec.ID, parentStatus, parentResult); cerr != nil {
		d.logger.Printf("resume parent %s: %v", parentRec.ID, cerr)
		return
	}

	parentGraph, err := d.fetchGraph(ctx, parentRec.GraphRunID)
	if err != nil {
		d.logger.Printf("load parent graph for %s: %v", parentRec.ID, err)
		return
	}
	// parentResult is assigned in every branch above (child failure,
	// projection failure, success) — no nil check needed.
	d.AdvanceAfterCompletion(ctx, parentGraph, parentRec.GraphRunID, parentRec.NodeID, parentStatus, parentResult.Error)
}

func childErrMessage(e *core.JobError) string {
	if e == nil {
		return "no error message"
	}
	return e.Error()
}

// projectChildOutputs reads the parent's pending_output_map (stashed by
// the subgraph module during its awaiting Execute) and reaches into the
// child's per-node records to build the parent's output port map.
func (d *Dispatcher) projectChildOutputs(
	ctx context.Context,
	parentRec core.JobRecord,
	childRunID string,
) (map[string]core.Ref, error) {
	if parentRec.Result == nil {
		return nil, fmt.Errorf("parent has no pending result")
	}
	rawJSON, _ := parentRec.Result.Output["pending_output_map"].Inline.(string)
	if rawJSON == "" {
		// No mapping declared — parent simply succeeds with no
		// outputs. Downstream edges from this parent's ports will be
		// dormant.
		return map[string]core.Ref{}, nil
	}
	var bindings map[string]subgraphOutputBinding
	if err := json.Unmarshal([]byte(rawJSON), &bindings); err != nil {
		return nil, fmt.Errorf("parse output_map: %w", err)
	}
	out := make(map[string]core.Ref, len(bindings))
	for parentPort, bind := range bindings {
		childNodeRec, err := d.store.Get(ctx, NodeJobID(childRunID, bind.Node))
		if err != nil {
			return nil, fmt.Errorf("child node %q: %w", bind.Node, err)
		}
		if childNodeRec.Result == nil {
			return nil, fmt.Errorf("child node %q has no result", bind.Node)
		}
		ref, ok := childNodeRec.Result.Output[bind.Port]
		if !ok {
			return nil, fmt.Errorf("child node %q has no output port %q", bind.Node, bind.Port)
		}
		out[parentPort] = ref
	}
	return out, nil
}

func (d *Dispatcher) fetchGraph(ctx context.Context, graphRunID string) (core.Graph, error) {
	return loadGraphFromRun(ctx, d.store, graphRunID)
}
