// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/engine"
)

// One worker goroutine; the pool size is the per-process step concurrency.
type WorkerConfig struct {
	ID              string
	PollInterval    time.Duration
	LeaseDuration   time.Duration
	LeaseRenewEvery time.Duration
	Logger          *log.Logger

	MaxRetries int

	RetryBackoff func(attempt int) time.Duration

	Metrics *Metrics

	Usage UsageStore

	// Called once the park has COMMITTED, so a fenced park does not notify — the
	// approval mail must go out exactly once per pause.
	OnNodeAwaiting func(ctx context.Context, graph core.Graph, runID, nodeID string, result core.Result)

	// A backstop for a node that declares none, so a hung drop cannot hold a worker
	// slot for ever.
	DefaultNodeTimeout time.Duration

	Runs *RunCache

	// Lets an enqueue cut the poll short, so a submit starts now.
	Wake *WorkSignal
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
	if out.DefaultNodeTimeout == 0 {
		out.DefaultNodeTimeout = 30 * time.Minute
	}
	if out.RetryBackoff == nil {
		out.RetryBackoff = func(attempt int) time.Duration {
			if attempt < 1 {
				attempt = 1
			}
			base := time.Second * time.Duration(1<<uint(attempt-1))
			// ±25% jitter, so siblings that fail together do not retry in lockstep.
			factor := 0.75 + rand.Float64()*0.5 // [0.75, 1.25)
			return time.Duration(float64(base) * factor)
		}
	}
	return out
}

type Worker struct {
	cfg        WorkerConfig
	store      core.JobStore
	engine     *engine.Engine
	bus        Bus
	dispatcher *Dispatcher
	graphs     *RunCache
	runState   runStateMeter
	// Without it, a module declaring SubmitsChildGraph cannot run.
	SubGraphRunner SubGraphRunner
}

func NewWorker(cfg WorkerConfig, store core.JobStore, eng *engine.Engine, bus Bus) *Worker {
	c := cfg.withDefaults()
	graphs := c.Runs
	if graphs == nil {
		graphs = NewRunCache(0)
	}
	return &Worker{
		cfg:        c,
		store:      store,
		engine:     eng,
		bus:        bus,
		dispatcher: NewDispatcher(store, bus, eng, c.Logger),
		graphs:     graphs,
	}
}

func (w *Worker) Run(ctx context.Context) error {
	w.cfg.Logger.Printf("[%s] started", w.cfg.ID)
	for {
		if err := ctx.Err(); err != nil {
			w.cfg.Logger.Printf("[%s] stopping: %v", w.cfg.ID, err)
			return err
		}
		// Taken BEFORE the claim: a waiter registered after an empty claim can miss a
		// wake that landed in between, and then sleeps the full interval.
		wake := w.cfg.Wake.Waiter()
		rec, err := w.store.Claim(ctx, w.cfg.ID, w.cfg.LeaseDuration)
		if errors.Is(err, core.ErrNoJobs) {
			if !waitForWork(ctx, wake, w.cfg.PollInterval) {
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
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func waitForWork(ctx context.Context, wake <-chan struct{}, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-wake:
		return true
	case <-t.C:
		return true
	}
}

func (w *Worker) processNodeJob(ctx context.Context, rec core.JobRecord) {
	// Detached from the claim context, so a shutdown cannot leave the node
	// terminal-written but its dependents unqueued.
	jobCtx := context.WithoutCancel(ctx)

	// A panic in node processing must fail the NODE, not the daemon: the engine's
	// recover only covers the calling goroutine.
	defer func() {
		if r := recover(); r != nil {
			w.cfg.Logger.Printf("[%s] PANIC processing node %s (run %s): %v\n%s",
				w.cfg.ID, rec.ID, rec.GraphRunID, r, debug.Stack())
			jerr := &core.JobError{Code: "panic", Message: "internal error processing this step"}
			fail := &core.Result{Status: core.StatusError, Error: jerr}
			if cerr := w.store.Complete(jobCtx, rec.ID, core.JobStatusFailed, fail); cerr != nil {
				w.cfg.Logger.Printf("[%s] panic-complete node %s: %v", w.cfg.ID, rec.ID, cerr)
			}
			if g, gerr := w.fetchGraph(jobCtx, rec.GraphRunID); gerr == nil {
				w.dispatcher.AdvanceAfterCompletion(jobCtx, g, rec.GraphRunID, rec.NodeID, core.JobStatusFailed, jerr)
			}
		}
	}()

	w.dispatcher.PublishNodeStatus(rec.GraphRunID, rec.NodeID, core.JobStatusRunning, nil)

	// Ties execution to the lease: if renewal finds the record reclaimed, the drop
	// is cancelled rather than left racing the new owner.
	execCtx, cancel := context.WithCancel(jobCtx)
	defer cancel()
	// The outbound choke point writes a server-requested delay here.
	execCtx, retryHint := core.WithRetryHint(execCtx)
	var leaseLost atomic.Bool
	var leaseWg sync.WaitGroup
	leaseWg.Add(1)
	go func() {
		defer leaseWg.Done()
		w.renewLease(execCtx, rec.ID, func() {
			leaseLost.Store(true)
			cancel()
		})
	}()
	stopLease := func() bool {
		cancel()
		leaseWg.Wait()
		return leaseLost.Load()
	}

	run, fetchErr := w.fetchRun(jobCtx, rec.GraphRunID)
	if fetchErr != nil {
		if stopLease() {
			w.cfg.Logger.Printf("[%s] %s: lease lost; abandoning (reclaimed elsewhere)", w.cfg.ID, rec.ID)
			return
		}
		w.failNode(jobCtx, rec, "load_graph", fetchErr.Error(), nil)
		return
	}
	graph := run.graph

	if node, ok := graph.Node(rec.NodeID); ok && node.Disabled {
		if stopLease() {
			w.cfg.Logger.Printf("[%s] %s: lease lost; abandoning (reclaimed elsewhere)", w.cfg.ID, rec.ID)
			return
		}
		if cerr := w.completeNode(jobCtx, rec.ID, core.JobStatusSkipped, nil); cerr != nil {
			w.cfg.Logger.Printf("[%s] skip disabled %s: %v", w.cfg.ID, rec.ID, cerr)
			return
		}
		w.cfg.Logger.Printf("[%s] %s skipped (step is switched off)", w.cfg.ID, rec.ID)
		w.dispatcher.PublishNodeStatus(rec.GraphRunID, rec.NodeID, core.JobStatusSkipped, nil)
		w.dispatcher.dispatchReady(jobCtx, graph, rec.GraphRunID, rec.NodeID)
		w.dispatcher.maybeCompleteGraph(jobCtx, graph, rec.GraphRunID, rec.NodeID, core.JobStatusSkipped, nil)
		return
	}

	prior, fetchErr := w.fetchPredecessors(jobCtx, graph, rec)
	if fetchErr == nil {
		w.addTemplateResults(jobCtx, graph, rec, prior)
	}
	if fetchErr != nil {
		if stopLease() {
			w.cfg.Logger.Printf("[%s] %s: lease lost; abandoning (reclaimed elsewhere)", w.cfg.ID, rec.ID)
			return
		}
		w.failNode(jobCtx, rec, "load_predecessors", fetchErr.Error(), &graph)
		return
	}

	nodeStart := time.Now()
	result, runErr := w.runNode(execCtx, graph, rec, prior)
	nodeElapsed := time.Since(nodeStart)
	if stopLease() {
		w.cfg.Logger.Printf("[%s] %s: lease lost during execution; abandoning (reclaimed elsewhere)", w.cfg.ID, rec.ID)
		return
	}

	if runErr == nil {
		if at, ok := core.ResumeAt(result); ok {
			if rerr := w.store.Requeue(jobCtx, rec.ID, at); rerr != nil {
				w.cfg.Logger.Printf("[%s] defer %s: %v", w.cfg.ID, rec.ID, rerr)
				return
			}
			w.cfg.Logger.Printf("[%s] %s deferred until %v; slot released",
				w.cfg.ID, rec.ID, at.Format(time.RFC3339Nano))
			return
		}
	}

	if runErr == nil && result.Status == core.StatusAwaiting {
		cerr := w.completeNode(jobCtx, rec.ID, core.JobStatusAwaiting, &result)
		if errors.Is(cerr, core.ErrConflict) {
			w.cfg.Logger.Printf("[%s] %s: park fenced (lease lost, already parked, or already terminal); abandoning", w.cfg.ID, rec.ID)
			return
		}
		if cerr != nil {
			w.cfg.Logger.Printf("[%s] park %s: %v", w.cfg.ID, rec.ID, cerr)
			return
		}
		w.cfg.Logger.Printf("[%s] parked %s awaiting external resume", w.cfg.ID, rec.ID)
		w.dispatcher.PublishNodeStatus(rec.GraphRunID, rec.NodeID, core.JobStatusAwaiting, nil)
		if isApprovalPause(&result) {
			setRunParked(jobCtx, w.store, w.cfg.Logger, rec.GraphRunID, true)
		}
		if graph, gerr := w.fetchGraph(jobCtx, rec.GraphRunID); gerr == nil {
			if w.cfg.OnNodeAwaiting != nil {
				w.cfg.OnNodeAwaiting(jobCtx, graph, rec.GraphRunID, rec.NodeID, result)
			}
			w.dispatcher.AdvanceAfterCompletion(jobCtx, graph, rec.GraphRunID, rec.NodeID, core.JobStatusAwaiting, nil)
		} else {
			w.cfg.Logger.Printf("[%s] park %s: could not load graph to notify dependents: %v", w.cfg.ID, rec.ID, gerr)
		}
		w.maybeSubmitChild(jobCtx, rec, result)
		return
	}

	status := core.JobStatusSucceeded
	if runErr != nil || result.Status == core.StatusError {
		status = core.JobStatusFailed
	}

	if status == core.JobStatusSucceeded {
		if total, ok := w.runState.charge(rec.GraphRunID, resultStateBytes(&result)); !ok {
			status = core.JobStatusFailed
			result = core.Result{
				JobID:  rec.ID,
				Status: core.StatusError,
				Error: &core.JobError{
					Code: "run_state_too_large",
					Message: fmt.Sprintf(
						"this run has stored about %d MiB of step results, over the %d MiB limit — "+
							"pass a reference or a single field between steps instead of a large value, "+
							"or raise DAZYFLOW_MAX_RUN_STATE_BYTES",
						total>>20, core.MaxRunStateBytes()>>20),
				},
			}
		}
	}

	if w.cfg.Metrics != nil {
		w.cfg.Metrics.ObserveNode(string(status), nodeElapsed.Seconds())
	}
	meterExecution := func() {
		if w.cfg.Usage == nil {
			return
		}
		if uerr := w.cfg.Usage.AddNodeExecutions(jobCtx, rec.Tenant, 1, time.Now()); uerr != nil {
			w.cfg.Logger.Printf("[%s] usage metering [%s]: count node execution: %v", w.cfg.ID, rec.Tenant, uerr)
		}
	}

	skipRetry := result.Error != nil && result.Error.Code == "timeout"

	if status == core.JobStatusFailed && !skipRetry {
		if when, reason := w.maybeScheduleRetry(graph, rec, retryHint.After()); !when.IsZero() {
			if err := w.store.Requeue(jobCtx, rec.ID, when); err == nil {
				meterExecution() // this attempt ran under our lease and is being retried
				w.cfg.Logger.Printf("[%s] retrying %s (attempt %d → next at %v)", w.cfg.ID, rec.ID, rec.Attempt, when.Format(time.RFC3339Nano))
				return
			} else {
				w.cfg.Logger.Printf("[%s] requeue %s failed (%v); falling back to terminal", w.cfg.ID, rec.ID, err)
			}
		} else if reason != "" {
			w.cfg.Logger.Printf("[%s] %s not retrying: %s", w.cfg.ID, rec.ID, reason)
		}
	}

	plan := w.dispatcher.PlanAdvance(jobCtx, graph, rec.GraphRunID, rec.NodeID, status, &result, run.manual)
	adv, cerr := w.completeAndEnqueue(jobCtx, rec.ID, status, &result, plan.enqueue)
	if errors.Is(cerr, core.ErrConflict) {
		w.cfg.Logger.Printf("[%s] %s: complete fenced (lease lost or already terminal); abandoning", w.cfg.ID, rec.ID)
		return
	}
	meterExecution()
	if cerr != nil {
		w.cfg.Logger.Printf("[%s] complete %s: %v", w.cfg.ID, rec.ID, cerr)
	}
	w.dispatcher.FinishAdvance(jobCtx, graph, rec.GraphRunID, rec.NodeID, status, &result, plan, adv)
}

func (w *Worker) completeAndEnqueue(ctx context.Context, jobID string, status core.JobStatus, result *core.Result, deps []core.JobRecord) (core.Advance, error) {
	if ce, ok := w.store.(core.CompleteEnqueuer); ok {
		return ce.CompleteAndEnqueue(ctx, jobID, w.cfg.ID, status, result, deps)
	}
	if err := w.completeNode(ctx, jobID, status, result); err != nil {
		return core.Advance{}, err
	}
	var adv core.Advance
	if len(deps) > 0 {
		if grec, err := w.store.Get(ctx, deps[0].GraphRunID); err == nil {
			adv.RunStatus = grec.Status
			if core.IsTerminalStatus(grec.Status) {
				return adv, nil
			}
		}
	}
	for _, d := range deps {
		switch err := w.store.Enqueue(ctx, d); {
		case err == nil:
			adv.Enqueued++
		case errors.Is(err, core.ErrConflict):
		default:
			w.cfg.Logger.Printf("[%s] enqueue dependent %s: %v", w.cfg.ID, d.NodeID, err)
		}
	}
	return adv, nil
}

func (w *Worker) completeNode(ctx context.Context, jobID string, status core.JobStatus, result *core.Result) error {
	if oc, ok := w.store.(core.OwnedCompleter); ok {
		return oc.CompleteOwned(ctx, jobID, w.cfg.ID, status, result)
	}
	return w.store.Complete(ctx, jobID, status, result)
}

func (w *Worker) maybeScheduleRetry(graph core.Graph, rec core.JobRecord, serverRetryAfter time.Duration) (time.Time, string) {
	node, ok := graph.Node(rec.NodeID)
	if !ok {
		return time.Time{}, "node missing from graph"
	}
	ctx := core.WithTenant(context.Background(), graph.Tenant)
	transport, err := w.engine.Resolver.Resolve(ctx, node.Module)
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

	if !manifest.Idempotent && !hasRetryEdge {
		return time.Time{}, "non-idempotent module retries only via an explicit on_error=retry edge"
	}

	attemptCap := w.cfg.MaxRetries
	if manifest.MaxRetries > 0 {
		attemptCap = manifest.MaxRetries
	}
	if rec.Attempt >= attemptCap {
		return time.Time{}, fmt.Sprintf("max retries (%d) reached", attemptCap)
	}

	delay := w.cfg.RetryBackoff(rec.Attempt)
	if serverRetryAfter > delay {
		delay = serverRetryAfter
	}
	return time.Now().Add(delay), ""
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
	defer func() {
		close(nodeProgress)
		<-forwardDone
	}()
	timeout := w.cfg.DefaultNodeTimeout
	if node, ok := graph.Node(rec.NodeID); ok && node.TimeoutSeconds > 0 {
		timeout = secondsToDuration(node.TimeoutSeconds)
	}
	ctx = core.WithTriggerDepth(ctx, w.runTriggerDepth(ctx, rec.GraphRunID))
	ctx = core.WithNodeEnqueuedAt(ctx, rec.EnqueuedAt)
	if node, ok := graph.Node(rec.NodeID); ok {
		ctx = core.WithNodeTimeout(ctx, secondsToDuration(node.TimeoutSeconds))
	}
	execCtx := ctx
	var cancelDeadline context.CancelFunc
	if timeout > 0 {
		execCtx, cancelDeadline = context.WithTimeout(ctx, timeout)
		defer cancelDeadline()
	}
	if node, ok := graph.Node(rec.NodeID); ok && node.Module == "for_each" {
		if body, isLoop := extractLoopBody(graph, rec.NodeID); isLoop {
			execCtx = engine.WithBodyRunner(execCtx, w.bodyRunner(body, rec.GraphRunID))
		}
	}
	result, err := w.engine.RunNode(execCtx, graph, rec.GraphRunID, rec.NodeID, rec.ID, prior, nodeProgress)

	if execCtx != ctx && errors.Is(execCtx.Err(), context.DeadlineExceeded) {
		return core.Result{
			JobID:  rec.ID,
			Status: core.StatusError,
			Error: &core.JobError{
				Code:    "timeout",
				Message: fmt.Sprintf("node exceeded %s timeout", timeout),
			},
		}, nil
	}
	return result, err
}

func (w *Worker) bodyRunner(body core.Graph, graphRunID string) engine.BodyRunner {
	bodyJSON, marshalErr := json.Marshal(body)
	var seq atomic.Int64
	return func(ctx context.Context, item core.Ref) (engine.GraphResult, error) {
		if marshalErr != nil {
			return engine.GraphResult{}, fmt.Errorf("clone loop body: %w", marshalErr)
		}
		var g core.Graph
		if err := json.Unmarshal(bodyJSON, &g); err != nil {
			return engine.GraphResult{}, fmt.Errorf("clone loop body: %w", err)
		}
		itemRunID := fmt.Sprintf("%s/i%d", graphRunID, seq.Add(1)-1)
		ctx = engine.WithLoopRunID(ctx, itemRunID)
		ctx = core.WithNodeEnqueuedAt(ctx, time.Time{})
		return w.engine.Run(engine.WithLoopItem(ctx, item.Inline), g, nil)
	}
}

func (w *Worker) fetchGraph(ctx context.Context, graphRunID string) (core.Graph, error) {
	run, err := w.fetchRun(ctx, graphRunID)
	return run.graph, err
}

func (w *Worker) fetchRun(ctx context.Context, graphRunID string) (cachedRun, error) {
	return w.graphs.runFor(ctx, w.store, graphRunID)
}

func (w *Worker) runTriggerDepth(ctx context.Context, graphRunID string) int {
	if run, ok := w.graphs.get(graphRunID); ok {
		return run.triggerDepth
	}
	rec, err := w.store.Get(ctx, graphRunID)
	if err != nil {
		return 0
	}
	return rec.TriggerDepth
}

func loadGraphFromRun(ctx context.Context, store core.JobStore, graphRunID string) (core.Graph, error) {
	g, _, err := loadRunFromStore(ctx, store, graphRunID)
	return g, err
}

func loadRunFromStore(ctx context.Context, store core.JobStore, graphRunID string) (core.Graph, core.JobRecord, error) {
	graphRec, err := loadRunRecord(ctx, store, graphRunID)
	if err != nil {
		return core.Graph{}, graphRec, err
	}
	var g core.Graph
	if err := json.Unmarshal(graphRec.GraphPayload, &g); err != nil {
		return core.Graph{}, graphRec, fmt.Errorf("unmarshal graph %s: %w", graphRunID, err)
	}
	return g, graphRec, nil
}

func loadRunRecord(ctx context.Context, store core.JobStore, graphRunID string) (core.JobRecord, error) {
	graphRec, err := store.Get(ctx, graphRunID)
	if err != nil {
		return core.JobRecord{}, fmt.Errorf("get graph-record %s: %w", graphRunID, err)
	}
	if len(graphRec.GraphPayload) == 0 {
		return graphRec, fmt.Errorf("graph-record %s has no payload", graphRunID)
	}
	return graphRec, nil
}

func (w *Worker) fetchPredecessors(ctx context.Context, graph core.Graph, rec core.JobRecord) (map[string]core.Result, error) {
	var from []string
	seen := make(map[string]struct{}, len(graph.Edges))
	for _, edge := range graph.Edges {
		if edge.To != rec.NodeID {
			continue
		}
		if _, dup := seen[edge.From]; dup {
			continue
		}
		seen[edge.From] = struct{}{}
		from = append(from, edge.From)
	}
	if len(from) == 0 {
		return map[string]core.Result{}, nil
	}

	outcomes, err := w.predecessorOutcomes(ctx, rec.GraphRunID, from)
	if err != nil {
		return nil, err
	}
	prior := make(map[string]core.Result, len(from))
	for _, nodeID := range from {
		oc, ok := outcomes[NodeJobID(rec.GraphRunID, nodeID)]
		if !ok {
			return nil, fmt.Errorf("predecessor %q: %w", nodeID, core.ErrNotFound)
		}
		switch oc.Status {
		case core.JobStatusFailed, core.JobStatusSkipped:
			continue
		}
		if oc.Result == nil {
			return nil, fmt.Errorf("predecessor %q has no result yet", nodeID)
		}
		prior[nodeID] = *oc.Result
	}
	return prior, nil
}

func (w *Worker) predecessorOutcomes(ctx context.Context, graphRunID string, nodeIDs []string) (map[string]core.NodeOutcome, error) {
	ids := make([]string, len(nodeIDs))
	for i, nodeID := range nodeIDs {
		ids[i] = NodeJobID(graphRunID, nodeID)
	}
	if reader, ok := w.store.(core.OutcomeReader); ok {
		return reader.Outcomes(ctx, ids)
	}
	out := make(map[string]core.NodeOutcome, len(ids))
	for i, id := range ids {
		predRec, err := w.store.Get(ctx, id)
		if errors.Is(err, core.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("predecessor %q: %w", nodeIDs[i], err)
		}
		out[id] = core.NodeOutcome{Status: predRec.Status, Result: predRec.Result}
	}
	return out, nil
}

func (w *Worker) maybeSubmitChild(ctx context.Context, rec core.JobRecord, result core.Result) {
	node, ok := w.lookupNode(rec)
	if !ok {
		return
	}
	transport, err := w.engine.Resolver.Resolve(core.WithTenant(ctx, rec.Tenant), node.Module)
	if err != nil {
		return
	}
	if !transport.Manifest().SubmitsChildGraph {
		return
	}
	if w.SubGraphRunner == nil {
		w.cfg.Logger.Printf("[%s] subgraph %s parked but no SubGraphRunner configured — child will not run", w.cfg.ID, rec.ID)
		return
	}
	childGraphID, _ := result.Output["pending_child_graph_id"].Inline.(string)
	seedsJSON, _ := result.Output["pending_input_seeds"].Inline.(string)
	if childGraphID == "" {
		w.cfg.Logger.Printf("[%s] subgraph %s: missing pending_child_graph_id", w.cfg.ID, rec.ID)
		return
	}
	var seeds map[string]core.Result
	if seedsJSON != "" {
		if err := json.Unmarshal([]byte(seedsJSON), &seeds); err != nil {
			w.cfg.Logger.Printf("[%s] subgraph %s: bad seeds payload: %v", w.cfg.ID, rec.ID, err)
			return
		}
	}
	childRunID, err := w.SubGraphRunner.SubmitChild(ctx, rec, childGraphID, seeds)
	if err != nil {
		w.cfg.Logger.Printf("[%s] subgraph %s: submit child %q: %v", w.cfg.ID, rec.ID, childGraphID, err)
		jerr := &core.JobError{Code: "subgraph_submit", Message: err.Error()}
		fail := &core.Result{Status: core.StatusError, Error: jerr}
		_ = w.store.Complete(context.WithoutCancel(ctx), rec.ID, core.JobStatusFailed, fail)
		if g, gerr := w.fetchGraph(context.WithoutCancel(ctx), rec.GraphRunID); gerr == nil {
			w.dispatcher.AdvanceAfterCompletion(context.WithoutCancel(ctx), g, rec.GraphRunID, rec.NodeID, core.JobStatusFailed, jerr)
		}
		return
	}
	w.cfg.Logger.Printf("[%s] subgraph %s submitted child %s (run=%s)", w.cfg.ID, rec.ID, childGraphID, childRunID)
}

func (w *Worker) lookupNode(rec core.JobRecord) (core.Node, bool) {
	g, err := w.fetchGraph(context.Background(), rec.GraphRunID)
	if err != nil {
		return core.Node{}, false
	}
	return g.Node(rec.NodeID)
}

func (w *Worker) failNode(ctx context.Context, rec core.JobRecord, code, msg string, graph *core.Graph) {
	ctx = context.WithoutCancel(ctx)
	jerr := &core.JobError{Code: code, Message: msg}
	result := &core.Result{Status: core.StatusError, Error: jerr}
	if cerr := w.store.Complete(ctx, rec.ID, core.JobStatusFailed, result); cerr != nil {
		w.cfg.Logger.Printf("[%s] complete-failure %s: %v", w.cfg.ID, rec.ID, cerr)
	}
	if graph != nil {
		w.dispatcher.AdvanceAfterCompletion(ctx, *graph, rec.GraphRunID, rec.NodeID, core.JobStatusFailed, jerr)
		return
	}
	w.dispatcher.PublishNodeStatus(rec.GraphRunID, rec.NodeID, core.JobStatusFailed, jerr)
	if cerr := w.store.Complete(ctx, rec.GraphRunID, core.JobStatusFailed, result); cerr == nil {
		w.bus.Publish(rec.GraphRunID, BusEvent{Terminal: &TerminalEvent{
			JobID:  rec.GraphRunID,
			Status: core.JobStatusFailed,
			Error:  jerr,
		}})
	}
}

func (w *Worker) renewLease(ctx context.Context, jobID string, onLost func()) {
	ticker := time.NewTicker(w.cfg.LeaseRenewEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			err := w.store.Renew(ctx, jobID, w.cfg.ID, w.cfg.LeaseDuration)
			if err == nil {
				continue
			}
			if errors.Is(err, core.ErrConflict) || errors.Is(err, core.ErrNotFound) {
				w.cfg.Logger.Printf("[%s] lost lease on %s (reclaimed elsewhere); fencing execution", w.cfg.ID, jobID)
				onLost()
				return
			}
			w.cfg.Logger.Printf("[%s] renew %s (transient): %v", w.cfg.ID, jobID, err)
		}
	}
}
