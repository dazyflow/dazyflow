// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/engine"
)

type subgraphOutputBinding struct {
	Node string `json:"node"`
	Port string `json:"port"`
}

type Dispatcher struct {
	store      core.JobStore
	bus        Bus
	engine     *engine.Engine
	logger     *log.Logger
	topologies topologyCache
}

func NewDispatcher(store core.JobStore, bus Bus, eng *engine.Engine, logger *log.Logger) *Dispatcher {
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	return &Dispatcher{store: store, bus: bus, engine: eng, logger: logger}
}

const reapBatchLimit = 500

// ReapStuckGraphRuns finalizes runs still marked running whose every node has
// reached a terminal state.
//
// Normally the dispatcher finalizes as a side effect of the last node's terminal
// transition. If a worker dies between that write and the completion check, the
// graph record is left running forever, with no lease and no node transition left
// to re-fire it. Re-running the completion check is a no-op for runs that
// genuinely still have work, and a clean finalize for those that are done.
//
// Safe on every replica concurrently: Complete is terminal-guarded, so only one
// finalize wins and a healthy in-flight run is never disturbed.
func (d *Dispatcher) ReapStuckGraphRuns(ctx context.Context) (int, error) {
	var runs []core.JobRecord
	for _, st := range []core.JobStatus{core.JobStatusRunning, core.JobStatusAwaiting} {
		batch, err := d.store.ListGraphRuns(ctx, core.ListGraphRunsOpts{
			Status: st,
			Limit:  reapBatchLimit,
		})
		if err != nil {
			return 0, err
		}
		runs = append(runs, batch...)
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
		d.maybeCompleteGraph(ctx, g, run.ID, "", core.JobStatusSucceeded, nil)
		rec, err := d.store.Get(ctx, run.ID)
		if err == nil && core.IsTerminalStatus(rec.Status) {
			reaped++
			d.logger.Printf("reaper: recovered orphaned graph run %s → %s", run.ID, rec.Status)
			continue
		}
		if err == nil && d.abandonIfStuck(ctx, g, rec) {
			reaped++
		}
	}
	return reaped, nil
}

// AbandonRunsAfter is not what decides it: abandonIfStuck only considers a run
// with NO pending step, which a waiting run always has. The window is there so a
// run mid-transition is never mistaken for an abandoned one.
var AbandonRunsAfter = 6 * time.Hour

// abandonIfStuck fails a run that can never finish.
//
// Retention is run-scoped now: an unfinished run is never pruned, which is right,
// but it means a run that can NEVER finish is immortal — it holds a concurrency
// slot for ever, so after a few of them every new run is admitted as pending and
// never starts, and nothing notifies because the run never reaches a terminal
// state. Two things produce one: a node record deleted by the old row-scoped
// retention, and a submission whose enqueue and Complete both failed.
//
// The test is not "old" but "nothing is pending". A run with any queued, running
// or awaiting record is waiting for something real — an approval parked three
// weeks, a delay counting down 90 days — and must never be touched.
func (d *Dispatcher) abandonIfStuck(ctx context.Context, graph core.Graph, run core.JobRecord) bool {
	if AbandonRunsAfter <= 0 {
		return false
	}
	age := time.Since(run.EnqueuedAt)
	if age < AbandonRunsAfter {
		return false
	}
	recs, err := d.store.ListNodeRecords(ctx, core.ListNodeRecordsOpts{
		Tenant:     run.Tenant,
		Workspace:  run.Workspace,
		GraphRunID: run.ID,
		Limit:      len(graph.Nodes) + 1,
	})
	if err != nil {
		return false // cannot tell; leave it alone
	}
	for _, r := range recs {
		if !core.IsTerminalStatus(r.Status) {
			return false // something is still pending: this run is alive
		}
	}
	msg := fmt.Sprintf("This run was abandoned: it has been %s since it started and it has no step "+
		"left that could finish it, so it can never complete. Its steps' records are gone or its "+
		"work was never queued. Retry it if the work still needs doing.", age.Round(time.Minute))
	if cerr := d.store.Complete(ctx, run.ID, core.JobStatusFailed, &core.Result{
		JobID:  run.ID,
		Status: core.StatusError,
		Error:  &core.JobError{Code: "run_abandoned", Message: msg},
	}); cerr != nil {
		return false
	}
	d.logger.Printf("reaper: abandoned graph run %s (%s old, %d node record(s), none pending)",
		run.ID, age.Round(time.Minute), len(recs))
	// Terminal now, so it stops counting against the concurrency cap, the
	// notification sweep tells the owner, and retention can age it out.
	d.bus.Publish(run.ID, BusEvent{Terminal: &TerminalEvent{
		JobID:  run.ID,
		Status: core.JobStatusFailed,
		Error:  &core.JobError{Code: "run_abandoned", Message: msg},
	}})
	return true
}

func (d *Dispatcher) AdvanceAfterCompletion(
	ctx context.Context,
	graph core.Graph,
	graphRunID, nodeID string,
	status core.JobStatus,
	resultErr *core.JobError,
) {
	d.PublishNodeStatus(graphRunID, nodeID, status, resultErr)
	// If the cancel path already marked the graph-record terminal it published its
	// own Terminal event, and the user's intent is "no more downstream work" — so a
	// node finishing mid-cancel must not enqueue dependents or double-publish.
	grec, grecErr := d.store.Get(ctx, graphRunID)
	if grecErr == nil && core.IsTerminalStatus(grec.Status) {
		return
	}
	// Only a run somebody is watching pauses on a breakpoint. An unreadable record
	// reads as "not watched": losing a pause is a worse debugging session, keeping one
	// on a triggered run is a run that never ends.
	watched := grecErr == nil && grec.Manual
	if pausesAfter(graph, graphRunID, nodeID, status, watched) {
		d.publishPaused(graphRunID, nodeID)
		return
	}
	enqueued := 0
	if d.advances(graph, nodeID, status) {
		enqueued = d.dispatchReady(ctx, graph, graphRunID, nodeID)
	}
	// Something was just queued, so the run is not finished and the completion check
	// would read every node record only to say so — one whole-run read per step on a
	// chain. The reaper re-runs it anyway for a run that somehow strands.
	if enqueued == 0 {
		d.maybeCompleteGraph(ctx, graph, graphRunID, nodeID, status, resultErr)
	}
}

// advancePlan is decided BEFORE the completion is written, so the store can
// commit both together — one commit per step instead of two, and no window with
// the node finished but its successor not yet queued.
//
// Sound for a dependent this node alone releases, and for one whose other
// predecessors are already terminal. NOT sound for a dependent still waiting on a
// step that may be finishing right now: the guarantee that at least one of two
// concurrent completions sees the other rests on each reading the other only
// after its own write is visible. So such a dependent is re-examined after the
// commit, on the ordinary read-then-dispatch path.
type advancePlan struct {
	pause   bool
	enqueue []core.JobRecord
	revisit bool
}

func (d *Dispatcher) PlanAdvance(
	ctx context.Context,
	graph core.Graph,
	graphRunID, nodeID string,
	status core.JobStatus,
	result *core.Result,
	watched bool,
) advancePlan {
	if pausesAfter(graph, graphRunID, nodeID, status, watched) {
		return advancePlan{pause: true}
	}
	if !d.advances(graph, nodeID, status) {
		return advancePlan{}
	}
	ix := d.indexFor(graphRunID, graph)
	ix.put(completedRecord(graphRunID, nodeID, status, result))
	var plan advancePlan
	for _, depID := range ix.outgoing[nodeID] {
		if _, owned := ix.bodyOwners[depID]; owned {
			continue
		}
		if decision, _ := d.analyzeDependentIndexed(ctx, graph, graphRunID, depID, ix); decision != depEnqueue {
			plan.revisit = true
			continue
		}
		plan.enqueue = append(plan.enqueue, dependentRecord(graph, graphRunID, depID))
	}
	return plan
}

func (d *Dispatcher) FinishAdvance(
	ctx context.Context,
	graph core.Graph,
	graphRunID, nodeID string,
	status core.JobStatus,
	result *core.Result,
	plan advancePlan,
	adv core.Advance,
) {
	var resultErr *core.JobError
	if result != nil {
		resultErr = result.Error
	}
	d.PublishNodeStatus(graphRunID, nodeID, status, resultErr)
	if core.IsTerminalStatus(adv.RunStatus) {
		return
	}
	if plan.pause {
		d.publishPaused(graphRunID, nodeID)
		return
	}
	enqueued := adv.Enqueued
	if plan.revisit {
		ix := d.indexFor(graphRunID, graph)
		ix.put(completedRecord(graphRunID, nodeID, status, result))
		enqueued += d.dispatchReadyIndexed(ctx, graph, graphRunID, nodeID, ix)
	}
	if enqueued == 0 {
		d.maybeCompleteGraph(ctx, graph, graphRunID, nodeID, status, resultErr)
	}
}

func (d *Dispatcher) advances(graph core.Graph, nodeID string, status core.JobStatus) bool {
	return status == core.JobStatusSucceeded || status == core.JobStatusAwaiting ||
		(status == core.JobStatusFailed && !d.failurePropagates(graph, nodeID))
}

func pausesAfter(graph core.Graph, graphRunID, nodeID string, status core.JobStatus, watched bool) bool {
	return status == core.JobStatusSucceeded && shouldPauseAfter(graph, graphRunID, nodeID, watched)
}

func (d *Dispatcher) publishPaused(graphRunID, nodeID string) {
	breakpoints.addPaused(graphRunID, nodeID)
	d.bus.Publish(graphRunID, BusEvent{Paused: &PausedEvent{
		NodeID:   nodeID,
		Stepping: breakpoints.isStepping(graphRunID),
	}})
}

func completedRecord(graphRunID, nodeID string, status core.JobStatus, result *core.Result) core.JobRecord {
	return core.JobRecord{
		ID:         NodeJobID(graphRunID, nodeID),
		Kind:       core.JobKindNode,
		GraphRunID: graphRunID,
		NodeID:     nodeID,
		Status:     status,
		Result:     result,
	}
}

func dependentRecord(graph core.Graph, graphRunID, nodeID string) core.JobRecord {
	return core.JobRecord{
		ID:         NodeJobID(graphRunID, nodeID),
		Kind:       core.JobKindNode,
		GraphRunID: graphRunID,
		GraphID:    graph.ID,
		NodeID:     nodeID,
		Tenant:     graph.Tenant,
		Workspace:  graph.Workspace,
		Job:        core.Job{GraphID: graph.ID, NodeID: nodeID},
	}
}

func (d *Dispatcher) resumeFrom(ctx context.Context, graph core.Graph, graphRunID string, nodeIDs []string) {
	for _, nodeID := range nodeIDs {
		d.dispatchReady(ctx, graph, graphRunID, nodeID)
		d.maybeCompleteGraph(ctx, graph, graphRunID, nodeID, core.JobStatusSucceeded, nil)
	}
}

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

func (d *Dispatcher) dispatchReady(ctx context.Context, graph core.Graph, graphRunID, completedNodeID string) int {
	return d.dispatchReadyIndexed(ctx, graph, graphRunID, completedNodeID, d.indexFor(graphRunID, graph))
}

// dispatchReadyIndexed uses a prebuilt edge index, which is what keeps dispatch
// linear in edge count: without it each dependent re-scanned every edge and
// re-read every predecessor record, so a densely wired flow cost O(nodes² ×
// edges) — minutes of CPU for a few hundred no-op steps.
//
// It returns how many dependents became NEW runnable work, which lets the caller
// skip the completion check: a run with something freshly queued cannot be
// finished, and confirming that costs a read of every node record in the run.
func (d *Dispatcher) dispatchReadyIndexed(
	ctx context.Context,
	graph core.Graph,
	graphRunID, completedNodeID string,
	ix *dispatchIndex,
) int {
	enqueued := 0
	// Loop-body nodes run once per item under their for_each, never standalone, so
	// the normal dispatcher skips them — including the for_each's own "body" edge.
	bodyOwners := ix.bodyOwners
	for _, nodeID := range ix.outgoing[completedNodeID] {
		if _, owned := bodyOwners[nodeID]; owned {
			continue
		}
		switch decision, reason := d.analyzeDependentIndexed(ctx, graph, graphRunID, nodeID, ix); decision {
		case depEnqueue:
			switch err := d.store.Enqueue(ctx, dependentRecord(graph, graphRunID, nodeID)); {
			case err == nil:
				enqueued++
			case errors.Is(err, core.ErrConflict):
				// A re-dispatch of a dependent that may already be terminal. Deliberately NOT
				// counted: treating it as new work would skip the completion check on a run that
				// really has finished, and leave it hanging.
			default:
				d.logger.Printf("enqueue dependent %s: %v", nodeID, err)
			}
		case depSkipped:
			d.recordSkippedIndexed(ctx, graph, graphRunID, nodeID, reason, ix)
		case depWaiting:
			// One line per dependent per pass floods the log on a wide flow — a 200-wire
			// fan-in wrote thousands in a second — and says nothing an operator acts on.
			if reason != "" && debugDispatch {
				d.logger.Printf("%s waiting: %s", nodeID, reason)
			}
		}
	}
	return enqueued
}

func (d *Dispatcher) recordSkippedIndexed(
	ctx context.Context,
	graph core.Graph,
	graphRunID, nodeID, reason string,
	ix *dispatchIndex,
) {
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
	ix.put(rec)
	d.dispatchReadyIndexed(ctx, graph, graphRunID, nodeID, ix)
	d.maybeCompleteGraph(ctx, graph, graphRunID, nodeID, core.JobStatusSkipped, nil)
}

type dependentDecision int

const (
	depWaiting dependentDecision = iota
	depEnqueue
	depSkipped
)

func (d *Dispatcher) analyzeDependent(ctx context.Context, graph core.Graph, graphRunID, depID string) (dependentDecision, string) {
	return d.analyzeDependentIndexed(ctx, graph, graphRunID, depID, d.indexFor(graphRunID, graph))
}

func (d *Dispatcher) analyzeDependentIndexed(
	ctx context.Context,
	graph core.Graph,
	graphRunID, depID string,
	ix *dispatchIndex,
) (dependentDecision, string) {
	var anyActive, anyBlocked, anyNotRouted bool
	var firstReason string
	for _, edge := range ix.incoming[depID] {
		predRec, err := ix.pred(ctx, d.store, graphRunID, edge.From)
		if err != nil {
			return depWaiting, fmt.Sprintf("predecessor %q not yet recorded", edge.From)
		}
		parked := predRec.Status == core.JobStatusAwaiting
		if !core.IsTerminalStatus(predRec.Status) && !parked {
			return depWaiting, fmt.Sprintf("predecessor %q is %s", edge.From, predRec.Status)
		}
		switch outcome := classifyEdge(predRec, edge); outcome {
		case edgeActive:
			anyActive = true
		case edgeDormant:
		case edgeNotRouted:
			// A path the predecessor declined. Recorded like a block so no other live wire
			// can run this step behind the router's back, but with its own reason.
			if !anyNotRouted && !anyBlocked {
				firstReason = fmt.Sprintf("predecessor %q did not route down %q",
					edge.From, edge.FromPort)
			}
			anyNotRouted = true
		case edgeBlocking:
			if parked {
				return depWaiting, fmt.Sprintf("predecessor %q is still waiting for its decision", edge.From)
			}
			if !anyBlocked {
				firstReason = fmt.Sprintf("predecessor %q is %s via %q edge",
					edge.From, predRec.Status, edge.OnError)
			}
			anyBlocked = true
		}
	}
	// A declined path skips the dependent even when another wire is live: routing is
	// exclusive, and a live value wire must not run the branch nobody chose.
	if anyBlocked || anyNotRouted {
		return depSkipped, firstReason
	}
	if !anyActive {
		return depSkipped, "all incoming edges dormant (fallback edges from succeeded preds)"
	}
	return depEnqueue, ""
}

type edgeOutcome int

const (
	edgeActive    edgeOutcome = iota // contributes to running this dependent
	edgeDormant                      // does not contribute but does not block either
	edgeNotRouted                    // pred succeeded but chose a different port: this path is not taken
	edgeBlocking                     // would prevent the dependent from running
)

func classifyEdge(predRec core.JobRecord, edge core.Edge) edgeOutcome {
	switch predRec.Status {
	case core.JobStatusSucceeded:
		if edge.OnError == core.OnErrorFallback {
			return edgeDormant
		}
		// The pass pin is a CONTROL pin: wiring it means "run after this step", whether
		// or not a value threaded through. Without this, a pass→pass sequencing wire from
		// a node with an empty pass-in reads as dormant and silently skips everything
		// downstream. The no-output dormancy below is for DATA routing.
		if edge.FromPort == core.PassPort {
			return edgeActive
		}
		// The predecessor chose which ports to emit on and left this one empty. That is
		// a ROUTING answer — "not down here" — so it must SKIP the dependent, not merely
		// fail to activate it. Treating it as dormant is what let a router leak: `if`
		// emits only `then`, but any OTHER live wire into the else-side step was enough to
		// enqueue it, so both branches ran.
		if predRec.Result == nil || predRec.Result.Output == nil {
			return edgeNotRouted
		}
		if _, ok := predRec.Result.Output[edge.FromPort]; !ok {
			return edgeNotRouted
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
	case core.JobStatusAwaiting:
		// A parked step has published what it could, and the whole point of an approval
		// link is to reach somebody WHILE the run waits. So an edge from a port it has
		// actually emitted is live now, while the ports arriving with the decision stay
		// blocked. Without this the documented pattern cannot work: the notification only
		// fired after the approval, so nobody was told there was something to approve.
		//
		// The pass pin means "run after this step", which a parked step has not done.
		if edge.FromPort == core.PassPort {
			return edgeBlocking
		}
		if predRec.Result == nil || predRec.Result.Output == nil {
			return edgeBlocking
		}
		if _, ok := predRec.Result.Output[edge.FromPort]; !ok {
			return edgeBlocking
		}
		return edgeActive
	default:
		return edgeBlocking
	}
}

func (d *Dispatcher) failurePropagates(graph core.Graph, nodeID string) bool {
	// A non-critical step never fails the run. Checked first because it must hold
	// for a TERMINAL step too, which by the edge rules below would always propagate,
	// having no outgoing edge to carry a policy.
	if n, ok := graph.Node(nodeID); ok && n.ContinueOnError {
		return false
	}
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

	// Loop-body nodes hold no record in the parent run and must not gate completion.
	// Read from the cached topology: re-deriving ownership on every node transition
	// walked the whole graph.
	bodyOwners := d.topologies.get(graphRunID, graph).bodyOwners
	nodeResults := make(map[string]core.Result, len(graph.Nodes))
	for _, n := range graph.Nodes {
		if _, owned := bodyOwners[n.ID]; owned {
			continue
		}
		rec, ok := byNode[n.ID]
		// Under-fetching can only err toward "not complete", never toward a false
		// completion.
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
	d.finalizeGraph(ctx, graph, graphRunID, core.JobStatusSucceeded, final,
		engine.GraphResult{
			GraphID: graph.ID,
			Status:  core.StatusOK,
			Nodes:   nodeResults,
		},
		core.StatusOK, nil)
}

// finalizeGraph persists the terminal record, clears pending breakpoints,
// reclaims scratch, publishes Terminal, and resumes a waiting parent. A no-op past
// the Complete call if the record was already terminal — Complete returning
// non-nil means another writer beat us, so we must not double-publish. The cancel
// path keeps its own ordering but shares reclaimScratch.
func (d *Dispatcher) finalizeGraph(
	ctx context.Context,
	graph core.Graph,
	graphRunID string,
	status core.JobStatus,
	result *core.Result,
	graphRes engine.GraphResult,
	resumeStatus string,
	resumeErr *core.JobError,
) {
	if cerr := d.store.Complete(ctx, graphRunID, status, result); cerr != nil {
		return
	}
	breakpoints.clear(graphRunID)
	d.reclaimScratch(graph, graphRunID)
	d.bus.Publish(graphRunID, BusEvent{Terminal: &TerminalEvent{
		JobID:    graphRunID,
		Status:   status,
		Error:    graphRes.Error,
		GraphRes: graphRes,
	}})
	d.maybeResumeParent(ctx, graphRunID, resumeStatus, resumeErr)
}

// reclaimScratch is best-effort: a failure is logged, never fatal, so it cannot
// block completion. A no-op without scratch support, or when the run created
// none.
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
	d.finalizeGraph(ctx, graph, graphRunID, core.JobStatusFailed, result,
		engine.GraphResult{
			GraphID: graph.ID,
			Status:  core.StatusError,
			Error:   errPayload,
		},
		core.StatusError, errPayload)
}

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
	d.AdvanceAfterCompletion(ctx, parentGraph, parentRec.GraphRunID, parentRec.NodeID, parentStatus, parentResult.Error)
}

func childErrMessage(e *core.JobError) string {
	if e == nil {
		return "no error message"
	}
	return e.Error()
}

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
