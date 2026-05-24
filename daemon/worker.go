package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/engine"
)

// WorkerConfig tunes a single worker goroutine. Production sets
// PollInterval low (so queued work is picked up promptly), LeaseDuration
// in tens of seconds (long enough to ride out brief stalls without
// holding a job hostage if the worker dies), and LeaseRenewEvery to
// ~one-third of LeaseDuration.
type WorkerConfig struct {
	ID              string
	PollInterval    time.Duration
	LeaseDuration   time.Duration
	LeaseRenewEvery time.Duration
	Logger          *log.Logger

	// MaxRetries caps how many total attempts a single node may get,
	// counting the initial run. Default 3. Set to 1 to disable retries.
	MaxRetries int

	// RetryBackoff returns the delay before the (attempt+1)-th try given
	// the count of attempts so far (1-indexed: 1 means "first try just
	// failed, picking delay before the second"). Default is
	// exponential: base*2^(attempt-1) with base=1s.
	RetryBackoff func(attempt int) time.Duration
}

func (c *WorkerConfig) withDefaults() WorkerConfig {
	out := *c
	if out.PollInterval == 0 {
		out.PollInterval = 100 * time.Millisecond
	}
	if out.LeaseDuration == 0 {
		out.LeaseDuration = 30 * time.Second
	}
	if out.LeaseRenewEvery == 0 {
		out.LeaseRenewEvery = 10 * time.Second
	}
	if out.Logger == nil {
		out.Logger = log.New(log.Writer(), "worker: ", log.LstdFlags)
	}
	if out.ID == "" {
		out.ID = "worker"
	}
	if out.MaxRetries == 0 {
		out.MaxRetries = 3
	}
	if out.RetryBackoff == nil {
		out.RetryBackoff = func(attempt int) time.Duration {
			if attempt < 1 {
				attempt = 1
			}
			return time.Second * time.Duration(1<<uint(attempt-1))
		}
	}
	return out
}

// Worker drains node-level jobs from a JobStore. Each iteration claims a
// single node job, executes it via Engine.RunNode, persists the result,
// and dispatches any newly-ready downstream nodes. Multiple Workers
// against the same store automatically share the load.
type Worker struct {
	cfg    WorkerConfig
	store  core.JobStore
	engine *engine.Engine
	bus    Bus
}

func NewWorker(cfg WorkerConfig, store core.JobStore, eng *engine.Engine, bus Bus) *Worker {
	return &Worker{
		cfg:    cfg.withDefaults(),
		store:  store,
		engine: eng,
		bus:    bus,
	}
}

func (w *Worker) Run(ctx context.Context) error {
	w.cfg.Logger.Printf("[%s] started", w.cfg.ID)
	for {
		if err := ctx.Err(); err != nil {
			w.cfg.Logger.Printf("[%s] stopping: %v", w.cfg.ID, err)
			return err
		}
		rec, err := w.store.Claim(ctx, w.cfg.ID, w.cfg.LeaseDuration)
		if errors.Is(err, core.ErrNoJobs) {
			if !sleepOrDone(ctx, w.cfg.PollInterval) {
				return ctx.Err()
			}
			continue
		}
		if err != nil {
			w.cfg.Logger.Printf("[%s] claim error: %v", w.cfg.ID, err)
			if !sleepOrDone(ctx, w.cfg.PollInterval) {
				return ctx.Err()
			}
			continue
		}
		w.processNodeJob(ctx, rec)
	}
}

func sleepOrDone(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// processNodeJob runs a single node job end-to-end.
func (w *Worker) processNodeJob(ctx context.Context, rec core.JobRecord) {
	leaseCtx, stopLease := context.WithCancel(ctx)
	defer stopLease()
	var leaseWg sync.WaitGroup
	leaseWg.Add(1)
	go func() {
		defer leaseWg.Done()
		w.renewLease(leaseCtx, rec.ID)
	}()

	graph, fetchErr := w.fetchGraph(ctx, rec.GraphRunID)
	if fetchErr != nil {
		stopLease()
		leaseWg.Wait()
		w.failNode(ctx, rec, "load_graph", fetchErr.Error(), nil)
		return
	}

	prior, fetchErr := w.fetchPredecessors(ctx, graph, rec)
	if fetchErr != nil {
		stopLease()
		leaseWg.Wait()
		w.failNode(ctx, rec, "load_predecessors", fetchErr.Error(), &graph)
		return
	}

	result, runErr := w.runNode(ctx, graph, rec, prior)
	stopLease()
	leaseWg.Wait()

	status := core.JobStatusSucceeded
	if runErr != nil || result.Status == core.StatusError {
		status = core.JobStatusFailed
	}

	if status == core.JobStatusFailed {
		if when, reason := w.maybeScheduleRetry(graph, rec); !when.IsZero() {
			if err := w.store.Requeue(context.Background(), rec.ID, when); err == nil {
				w.cfg.Logger.Printf("[%s] retrying %s (attempt %d → next at %v)", w.cfg.ID, rec.ID, rec.Attempt, when.Format(time.RFC3339Nano))
				return
			} else {
				w.cfg.Logger.Printf("[%s] requeue %s failed (%v); falling back to terminal", w.cfg.ID, rec.ID, err)
			}
		} else if reason != "" {
			w.cfg.Logger.Printf("[%s] %s not retrying: %s", w.cfg.ID, rec.ID, reason)
		}
	}

	if cerr := w.store.Complete(context.Background(), rec.ID, status, &result); cerr != nil {
		w.cfg.Logger.Printf("[%s] complete %s: %v", w.cfg.ID, rec.ID, cerr)
	}

	// Dispatch dependents when the outcome can let downstream proceed:
	// either a succeeded run, or a failure whose outgoing edges are all
	// OnError=skip (a "tolerated" failure).
	if status == core.JobStatusSucceeded ||
		(status == core.JobStatusFailed && !w.failurePropagates(graph, rec.NodeID)) {
		w.dispatchReady(ctx, graph, rec.GraphRunID, rec.NodeID)
	}
	w.maybeCompleteGraph(ctx, graph, rec.GraphRunID, rec.NodeID, status, result.Error)
}

// maybeScheduleRetry returns the time at which to retry the failed node,
// or the zero time when no retry should happen. The decision honors:
//   - manifest.RetryPolicy (must be set; only exponential_backoff is
//     implemented today)
//   - at least one outgoing edge with on_error=retry (or no outgoing
//     edges at all — leaf nodes get retry on manifest alone)
//   - the cap from WorkerConfig.MaxRetries against rec.Attempt
func (w *Worker) maybeScheduleRetry(graph core.Graph, rec core.JobRecord) (time.Time, string) {
	node, ok := graph.Node(rec.NodeID)
	if !ok {
		return time.Time{}, "node missing from graph"
	}
	transport, err := w.engine.Resolver.Resolve(node.Module)
	if err != nil {
		return time.Time{}, "module not resolvable"
	}
	manifest := transport.Manifest()
	if manifest.RetryPolicy != core.RetryExponentialBackoff {
		return time.Time{}, "manifest has no retry policy"
	}

	var hasOutgoing, hasRetryEdge bool
	for _, edge := range graph.Edges {
		if edge.From != rec.NodeID {
			continue
		}
		hasOutgoing = true
		if edge.OnError == core.OnErrorRetry {
			hasRetryEdge = true
			break
		}
	}
	if hasOutgoing && !hasRetryEdge {
		return time.Time{}, "no outgoing edge requests retry"
	}

	if rec.Attempt >= w.cfg.MaxRetries {
		return time.Time{}, fmt.Sprintf("max retries (%d) reached", w.cfg.MaxRetries)
	}

	return time.Now().Add(w.cfg.RetryBackoff(rec.Attempt)), ""
}

func (w *Worker) runNode(ctx context.Context, graph core.Graph, rec core.JobRecord, prior map[string]core.Result) (core.Result, error) {
	nodeProgress := make(chan core.Progress, 16)
	forwardDone := make(chan struct{})
	go func() {
		defer close(forwardDone)
		for p := range nodeProgress {
			pCopy := p
			w.bus.Publish(rec.GraphRunID, BusEvent{Progress: &engine.GraphProgress{
				JobID:    rec.ID,
				NodeID:   rec.NodeID,
				Progress: pCopy,
			}})
		}
	}()
	result, err := w.engine.RunNode(ctx, graph, rec.NodeID, prior, nodeProgress)
	close(nodeProgress)
	<-forwardDone
	return result, err
}

// fetchGraph loads the graph payload from the graph-record.
func (w *Worker) fetchGraph(ctx context.Context, graphRunID string) (core.Graph, error) {
	graphRec, err := w.store.Get(ctx, graphRunID)
	if err != nil {
		return core.Graph{}, fmt.Errorf("get graph-record %s: %w", graphRunID, err)
	}
	if len(graphRec.GraphPayload) == 0 {
		return core.Graph{}, fmt.Errorf("graph-record %s has no payload", graphRunID)
	}
	var g core.Graph
	if err := json.Unmarshal(graphRec.GraphPayload, &g); err != nil {
		return core.Graph{}, fmt.Errorf("unmarshal graph %s: %w", graphRunID, err)
	}
	return g, nil
}

// fetchPredecessors collects the Result of every node that feeds into rec.NodeID,
// keyed by upstream node ID so engine.assembleInput can look them up.
// Predecessors that didn't produce data (failed or skipped) are silently
// omitted — analyzeDependent has already verified those non-success
// states are tolerated by the edge's on_error, so omission means the
// downstream module sees no value on those input ports.
func (w *Worker) fetchPredecessors(ctx context.Context, graph core.Graph, rec core.JobRecord) (map[string]core.Result, error) {
	prior := make(map[string]core.Result)
	for _, edge := range graph.Edges {
		if edge.To != rec.NodeID {
			continue
		}
		if _, already := prior[edge.From]; already {
			continue
		}
		predID := NodeJobID(rec.GraphRunID, edge.From)
		predRec, err := w.store.Get(ctx, predID)
		if err != nil {
			return nil, fmt.Errorf("predecessor %q: %w", edge.From, err)
		}
		switch predRec.Status {
		case core.JobStatusFailed, core.JobStatusSkipped:
			continue
		}
		if predRec.Result == nil {
			return nil, fmt.Errorf("predecessor %q has no result yet", edge.From)
		}
		prior[edge.From] = *predRec.Result
	}
	return prior, nil
}

// dispatchReady walks dependents of completedNodeID and, for each, decides
// whether to enqueue it, mark it skipped, or wait. The Skipped path also
// cascades: marking D skipped triggers a fresh dispatch from D so that
// D's own downstream resolves (either enqueueing or further-skipping).
func (w *Worker) dispatchReady(ctx context.Context, graph core.Graph, graphRunID, completedNodeID string) {
	dependents := map[string]struct{}{}
	for _, edge := range graph.Edges {
		if edge.From == completedNodeID {
			dependents[edge.To] = struct{}{}
		}
	}
	for nodeID := range dependents {
		switch decision, reason := w.analyzeDependent(ctx, graph, graphRunID, nodeID); decision {
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
			if err := w.store.Enqueue(ctx, newRec); err != nil && !errors.Is(err, core.ErrConflict) {
				w.cfg.Logger.Printf("[%s] enqueue dependent %s: %v", w.cfg.ID, nodeID, err)
			}
		case depSkipped:
			w.recordSkipped(ctx, graph, graphRunID, nodeID, reason)
		case depWaiting:
			if reason != "" {
				w.cfg.Logger.Printf("[%s] %s waiting: %s", w.cfg.ID, nodeID, reason)
			}
		}
	}
}

// recordSkipped writes a terminal Skipped record for a node that won't
// run, then cascades: dispatches from the skipped node so transitively
// blocked downstream is also resolved, and bumps the graph-completion
// check in case this was the last outstanding node.
func (w *Worker) recordSkipped(ctx context.Context, graph core.Graph, graphRunID, nodeID, reason string) {
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
	if err := w.store.Enqueue(ctx, rec); err != nil {
		if !errors.Is(err, core.ErrConflict) {
			w.cfg.Logger.Printf("[%s] record skipped %s: %v", w.cfg.ID, nodeID, err)
		}
		return
	}
	w.cfg.Logger.Printf("[%s] skipped %s: %s", w.cfg.ID, nodeID, reason)
	w.dispatchReady(ctx, graph, graphRunID, nodeID)
	w.maybeCompleteGraph(ctx, graph, graphRunID, nodeID, core.JobStatusSkipped, nil)
}

type dependentDecision int

const (
	depWaiting dependentDecision = iota
	depEnqueue
	depSkipped
)

// analyzeDependent classifies an incoming edge against its predecessor's
// terminal state. Per-edge outcomes combine into a final decision: any
// blocking edge or absence of an active edge means the dependent is
// skipped; any waiting edge defers the decision.
func (w *Worker) analyzeDependent(ctx context.Context, graph core.Graph, graphRunID, depID string) (dependentDecision, string) {
	var anyActive, anyBlocked bool
	var firstReason string
	for _, edge := range graph.Edges {
		if edge.To != depID {
			continue
		}
		predRec, err := w.store.Get(ctx, NodeJobID(graphRunID, edge.From))
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

// classifyEdge maps (predecessor terminal record, edge) → outcome. The
// truth table covers the four terminal states crossed with the four
// OnError values, plus an additional "did the source actually emit on
// this FromPort?" check for succeeded sources — required so that a
// branch-style module that emits on only one of several output ports
// correctly skips the dependents wired to its unused ports.
func classifyEdge(predRec core.JobRecord, edge core.Edge) edgeOutcome {
	switch predRec.Status {
	case core.JobStatusSucceeded:
		if edge.OnError == core.OnErrorFallback {
			return edgeDormant
		}
		// If the predecessor didn't actually emit on the port this
		// edge reads from, treat the edge as dormant — there's no
		// data to flow.
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

// failurePropagates reports whether a failure of nodeID should abort the
// graph. The rule is asymmetric between skip and fallback:
//
//   - fallback is an explicit "alternative handler" declaration; if a
//     fallback edge exists, the graph survives regardless of other
//     outgoing edges. Paths going through abort-edges become skipped.
//   - skip only tolerates failure locally. With [skip→A, abort→B] the
//     abort sibling still propagates and the graph fails.
//   - a leaf (no outgoing edges) defaults to abort.
func (w *Worker) failurePropagates(graph core.Graph, nodeID string) bool {
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
			// tolerated locally; doesn't on its own block propagation
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

// maybeCompleteGraph updates the graph-record's terminal state. The first
// worker to win the race on Complete also publishes the terminal bus
// event; later attempts get ErrConflict and stay quiet.
//
// Three outcomes are possible:
//   - last node failed AND failure propagates → graph fails immediately
//   - all nodes terminal AND none has a propagating failure → graph
//     succeeds (failed-via-skip nodes are tolerated)
//   - some node still pending → return; another invocation will close it
func (w *Worker) maybeCompleteGraph(
	ctx context.Context,
	graph core.Graph,
	graphRunID, lastNodeID string,
	lastStatus core.JobStatus,
	lastErr *core.JobError,
) {
	if lastStatus == core.JobStatusFailed && w.failurePropagates(graph, lastNodeID) {
		w.markGraphFailed(ctx, graph, graphRunID, lastNodeID, lastErr)
		return
	}

	nodeResults := make(map[string]core.Result, len(graph.Nodes))
	for _, n := range graph.Nodes {
		rec, err := w.store.Get(ctx, NodeJobID(graphRunID, n.ID))
		if err != nil {
			return
		}
		if !core.IsTerminalStatus(rec.Status) {
			return
		}
		if rec.Status == core.JobStatusFailed && w.failurePropagates(graph, n.ID) {
			// Defensive: should have been caught when that node failed.
			var perr *core.JobError
			if rec.Result != nil {
				perr = rec.Result.Error
			}
			w.markGraphFailed(ctx, graph, graphRunID, n.ID, perr)
			return
		}
		if rec.Result != nil {
			nodeResults[n.ID] = *rec.Result
		}
	}

	final := &core.Result{Status: core.StatusOK}
	if cerr := w.store.Complete(ctx, graphRunID, core.JobStatusSucceeded, final); cerr == nil {
		w.bus.Publish(graphRunID, BusEvent{Terminal: &TerminalEvent{
			JobID:  graphRunID,
			Status: core.JobStatusSucceeded,
			GraphRes: engine.GraphResult{
				GraphID: graph.ID,
				Status:  core.StatusOK,
				Nodes:   nodeResults,
			},
		}})
	}
}

func (w *Worker) markGraphFailed(
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
	if cerr := w.store.Complete(ctx, graphRunID, core.JobStatusFailed, result); cerr == nil {
		w.bus.Publish(graphRunID, BusEvent{Terminal: &TerminalEvent{
			JobID:  graphRunID,
			Status: core.JobStatusFailed,
			Error:  errPayload,
			GraphRes: engine.GraphResult{
				GraphID: graph.ID,
				Status:  core.StatusError,
				Error:   errPayload,
			},
		}})
	}
}

func (w *Worker) failNode(ctx context.Context, rec core.JobRecord, code, msg string, graph *core.Graph) {
	jerr := &core.JobError{Code: code, Message: msg}
	result := &core.Result{Status: core.StatusError, Error: jerr}
	if cerr := w.store.Complete(context.Background(), rec.ID, core.JobStatusFailed, result); cerr != nil {
		w.cfg.Logger.Printf("[%s] complete-failure %s: %v", w.cfg.ID, rec.ID, cerr)
	}
	if graph != nil {
		w.maybeCompleteGraph(ctx, *graph, rec.GraphRunID, rec.NodeID, core.JobStatusFailed, jerr)
		return
	}
	// We never even loaded the graph, so we can't walk for completion.
	// Mark the graph-record as failed best-effort.
	if cerr := w.store.Complete(context.Background(), rec.GraphRunID, core.JobStatusFailed, result); cerr == nil {
		w.bus.Publish(rec.GraphRunID, BusEvent{Terminal: &TerminalEvent{
			JobID:  rec.GraphRunID,
			Status: core.JobStatusFailed,
			Error:  jerr,
		}})
	}
}

func (w *Worker) renewLease(ctx context.Context, jobID string) {
	ticker := time.NewTicker(w.cfg.LeaseRenewEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := w.store.Renew(ctx, jobID, w.cfg.ID, w.cfg.LeaseDuration); err != nil {
				w.cfg.Logger.Printf("[%s] renew %s: %v", w.cfg.ID, jobID, err)
			}
		}
	}
}
