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
// and dispatches any newly-ready downstream nodes via the shared
// Dispatcher (which Service.Approve also uses). Multiple Workers against
// the same store automatically share the load.
type Worker struct {
	cfg        WorkerConfig
	store      core.JobStore
	engine     *engine.Engine
	bus        Bus
	dispatcher *Dispatcher
	// SubGraphRunner is optional. When nil and a module's manifest has
	// SubmitsChildGraph=true, the worker still parks the node but logs
	// a warning — the graph will hang because no one will submit the
	// child. Production deployments must wire this (Service satisfies
	// the interface).
	SubGraphRunner SubGraphRunner
}

func NewWorker(cfg WorkerConfig, store core.JobStore, eng *engine.Engine, bus Bus) *Worker {
	c := cfg.withDefaults()
	return &Worker{
		cfg:        c,
		store:      store,
		engine:     eng,
		bus:        bus,
		dispatcher: NewDispatcher(store, bus, eng, c.Logger),
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
	// Announce the transition into "running" right after the claim so the
	// UI's per-node dot lights up before Execute returns.
	w.dispatcher.PublishNodeStatus(rec.GraphRunID, rec.NodeID, core.JobStatusRunning, nil)

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

	// Pause path: the module asked to be parked until an external
	// resume call. Write status=awaiting, drop the lease, and stop —
	// do NOT dispatch dependents or check graph completion. The
	// resume call (Service.Approve, or — for subgraph nodes — the
	// dispatcher when the child terminates) is what advances those.
	if runErr == nil && result.Status == core.StatusAwaiting {
		if cerr := w.store.Complete(context.Background(), rec.ID, core.JobStatusAwaiting, &result); cerr != nil {
			w.cfg.Logger.Printf("[%s] park %s: %v", w.cfg.ID, rec.ID, cerr)
			return
		}
		w.cfg.Logger.Printf("[%s] parked %s awaiting external resume", w.cfg.ID, rec.ID)
		// Park is a real status transition — the UI wants to show
		// "awaiting" on this node while it sits.
		w.dispatcher.PublishNodeStatus(rec.GraphRunID, rec.NodeID, core.JobStatusAwaiting, nil)
		// If the manifest declares it submits a child graph, hand the
		// result off to the SubGraphRunner now. The dispatcher will
		// resume the parent when the child terminates.
		w.maybeSubmitChild(ctx, rec, result)
		return
	}

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

	// Dispatch dependents + check graph completion via the shared
	// dispatcher (used by both worker and approval path).
	w.dispatcher.AdvanceAfterCompletion(ctx, graph, rec.GraphRunID, rec.NodeID, status, result.Error)
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
	result, err := w.engine.RunNode(ctx, graph, rec.GraphRunID, rec.NodeID, prior, nodeProgress)
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

// maybeSubmitChild checks whether the parked node was produced by a
// subgraph-style module and, if so, parses the metadata embedded in its
// Result and asks the runner to submit the child graph.
//
// We re-resolve the manifest here rather than threading it through
// processNodeJob — the registry lookup is cheap and keeps the awaiting
// branch self-contained.
func (w *Worker) maybeSubmitChild(ctx context.Context, rec core.JobRecord, result core.Result) {
	node, ok := w.lookupNode(rec)
	if !ok {
		return
	}
	transport, err := w.engine.Resolver.Resolve(node.Module)
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
		// Fail the parent — without a child, it'll hang forever.
		jerr := &core.JobError{Code: "subgraph_submit", Message: err.Error()}
		fail := &core.Result{Status: core.StatusError, Error: jerr}
		// Force-complete the parent: the awaiting record is still
		// resumable via Complete because we excluded awaiting from the
		// terminal guard.
		_ = w.store.Complete(context.Background(), rec.ID, core.JobStatusFailed, fail)
		if g, gerr := w.fetchGraph(context.Background(), rec.GraphRunID); gerr == nil {
			w.dispatcher.AdvanceAfterCompletion(ctx, g, rec.GraphRunID, rec.NodeID, core.JobStatusFailed, jerr)
		}
		return
	}
	w.cfg.Logger.Printf("[%s] subgraph %s submitted child %s (run=%s)", w.cfg.ID, rec.ID, childGraphID, childRunID)
}

// lookupNode finds the parent record's node definition in its graph
// payload. Returns ok=false on any error — callers treat that as "no
// subgraph linkage to set up."
func (w *Worker) lookupNode(rec core.JobRecord) (core.Node, bool) {
	g, err := w.fetchGraph(context.Background(), rec.GraphRunID)
	if err != nil {
		return core.Node{}, false
	}
	return g.Node(rec.NodeID)
}

func (w *Worker) failNode(ctx context.Context, rec core.JobRecord, code, msg string, graph *core.Graph) {
	jerr := &core.JobError{Code: code, Message: msg}
	result := &core.Result{Status: core.StatusError, Error: jerr}
	if cerr := w.store.Complete(context.Background(), rec.ID, core.JobStatusFailed, result); cerr != nil {
		w.cfg.Logger.Printf("[%s] complete-failure %s: %v", w.cfg.ID, rec.ID, cerr)
	}
	if graph != nil {
		w.dispatcher.AdvanceAfterCompletion(ctx, *graph, rec.GraphRunID, rec.NodeID, core.JobStatusFailed, jerr)
		return
	}
	// We never even loaded the graph, so we can't walk for completion.
	// Publish a node-status anyway so the UI still sees the failure;
	// mark the graph-record as failed best-effort.
	w.dispatcher.PublishNodeStatus(rec.GraphRunID, rec.NodeID, core.JobStatusFailed, jerr)
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
