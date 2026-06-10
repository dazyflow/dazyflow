package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/engine"
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

	// Metrics, when set, receives per-node execution latency (keyed by
	// terminal status) for the /metrics endpoint. Nil disables it.
	Metrics *Metrics

	// DefaultNodeTimeout is the wall-time backstop applied to a node that
	// sets no explicit TimeoutSeconds. Without it, a node that honors
	// cancellation but never finishes on its own — a remote gRPC call to a
	// black-hole host, a native HTTP/DB call to a stalled backend — would
	// hold its worker slot until the lease churns. The deadline cancels its
	// context so it returns. (Code that actively ignores ctx can't be
	// interrupted — Go can't kill a goroutine — so every drop is trusted,
	// first-party Go that honours its context.) A node's own TimeoutSeconds,
	// when set, overrides this. It's a generous backstop, not an SLA:
	// parked nodes (await_approval, subgraph) return from Execute promptly
	// and so never approach it. Default 30m; set negative to disable.
	DefaultNodeTimeout time.Duration
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
			// ±25% jitter so a wave of sibling nodes that fail together
			// (e.g. a shared dependency blips) don't all retry on the
			// same tick and re-synchronize the thundering herd.
			factor := 0.75 + rand.Float64()*0.5 // [0.75, 1.25)
			return time.Duration(float64(base) * factor)
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

	// execCtx ties the node's execution to the lease. If renewal detects
	// the lease was lost (another worker reclaimed an expired job), the
	// renew goroutine flips leaseLost and cancels execCtx to abort the
	// in-flight run; we then abandon the job without writing a result, so
	// the new owner's run is authoritative (no double-write, best-effort
	// no double-execution for ctx-respecting modules).
	//
	// It's derived via WithoutCancel so a graceful shutdown (the claim-loop
	// ctx being cancelled on SIGTERM) does NOT abort a node mid-run: the
	// claim loop stops taking new work, but the node already in flight runs
	// to its natural completion (bounded by its own timeout/lease and the
	// caller's bounded drain). Lease loss still cancels it via the explicit
	// cancel() below.
	execCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	defer cancel()
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
	// stopLease halts the renew goroutine and reports whether the lease
	// was lost during this attempt.
	stopLease := func() bool {
		cancel()
		leaseWg.Wait()
		return leaseLost.Load()
	}

	graph, fetchErr := w.fetchGraph(ctx, rec.GraphRunID)
	if fetchErr != nil {
		if stopLease() {
			w.cfg.Logger.Printf("[%s] %s: lease lost; abandoning (reclaimed elsewhere)", w.cfg.ID, rec.ID)
			return
		}
		w.failNode(ctx, rec, "load_graph", fetchErr.Error(), nil)
		return
	}

	// Disabled switch: the node is off — record it as skipped without
	// executing. dispatchReady then evaluates its dependents, and the
	// standard skip cascade (skipped predecessor blocks a default edge)
	// prunes everything downstream; maybeCompleteGraph still completes the
	// run since skipped is terminal.
	if node, ok := graph.Node(rec.NodeID); ok && node.Disabled {
		if stopLease() {
			w.cfg.Logger.Printf("[%s] %s: lease lost; abandoning (reclaimed elsewhere)", w.cfg.ID, rec.ID)
			return
		}
		if cerr := w.completeNode(ctx, rec.ID, core.JobStatusSkipped, nil); cerr != nil {
			w.cfg.Logger.Printf("[%s] skip disabled %s: %v", w.cfg.ID, rec.ID, cerr)
			return
		}
		w.cfg.Logger.Printf("[%s] %s skipped (step is switched off)", w.cfg.ID, rec.ID)
		w.dispatcher.PublishNodeStatus(rec.GraphRunID, rec.NodeID, core.JobStatusSkipped, nil)
		w.dispatcher.dispatchReady(ctx, graph, rec.GraphRunID, rec.NodeID)
		w.dispatcher.maybeCompleteGraph(ctx, graph, rec.GraphRunID, rec.NodeID, core.JobStatusSkipped, nil)
		return
	}

	prior, fetchErr := w.fetchPredecessors(ctx, graph, rec)
	if fetchErr != nil {
		if stopLease() {
			w.cfg.Logger.Printf("[%s] %s: lease lost; abandoning (reclaimed elsewhere)", w.cfg.ID, rec.ID)
			return
		}
		w.failNode(ctx, rec, "load_predecessors", fetchErr.Error(), &graph)
		return
	}

	nodeStart := time.Now()
	result, runErr := w.runNode(execCtx, graph, rec, prior)
	nodeElapsed := time.Since(nodeStart)
	if stopLease() {
		// Lost the lease mid-execution → another worker owns this job now.
		// Abandon: writing a terminal result here would clobber the new
		// owner's run, and retrying/dispatching would duplicate work.
		w.cfg.Logger.Printf("[%s] %s: lease lost during execution; abandoning (reclaimed elsewhere)", w.cfg.ID, rec.ID)
		return
	}

	// Pause path: the module asked to be parked until an external
	// resume call. Write status=awaiting, drop the lease, and stop —
	// do NOT dispatch dependents or check graph completion. The
	// resume call (Service.Approve, or — for subgraph nodes — the
	// dispatcher when the child terminates) is what advances those.
	if runErr == nil && result.Status == core.StatusAwaiting {
		cerr := w.completeNode(context.Background(), rec.ID, core.JobStatusAwaiting, &result)
		if errors.Is(cerr, core.ErrConflict) {
			w.cfg.Logger.Printf("[%s] %s: park fenced (lease lost or already terminal); abandoning", w.cfg.ID, rec.ID)
			return
		}
		if cerr != nil {
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

	// Record execution latency for any node that reached a terminal
	// status (the awaiting/park path returned above). Failed attempts
	// that will retry are counted too — they're real executions.
	if w.cfg.Metrics != nil {
		w.cfg.Metrics.ObserveNode(string(status), nodeElapsed.Seconds())
	}

	// Timeouts are intentional caps, not transient blips — retrying
	// would just waste the next budget too. Skip retry whenever the
	// failure carries the synthesized timeout code so a 1s cap means 1s.
	skipRetry := result.Error != nil && result.Error.Code == "timeout"

	if status == core.JobStatusFailed && !skipRetry {
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

	cerr := w.completeNode(context.Background(), rec.ID, status, &result)
	if errors.Is(cerr, core.ErrConflict) {
		// Fenced: we lost the lease (reclaimed elsewhere) or the record is
		// already terminal. Abandon — don't advance dependents off our
		// outcome; the owner that wrote the terminal state advances.
		w.cfg.Logger.Printf("[%s] %s: complete fenced (lease lost or already terminal); abandoning", w.cfg.ID, rec.ID)
		return
	}
	if cerr != nil {
		w.cfg.Logger.Printf("[%s] complete %s: %v", w.cfg.ID, rec.ID, cerr)
	}

	// Dispatch dependents + check graph completion via the shared
	// dispatcher (used by both worker and approval path). Detached from the
	// claim ctx (WithoutCancel) so that when a node finishes during a
	// graceful shutdown its dependents still get enqueued and the graph
	// completion check still runs — otherwise the run would strand with
	// dependents never created (and the reaper, which only finalizes runs
	// whose nodes are all terminal, couldn't recover it).
	w.dispatcher.AdvanceAfterCompletion(context.WithoutCancel(ctx), graph, rec.GraphRunID, rec.NodeID, status, result.Error)
}

// completeNode writes a node's terminal/awaiting status, fenced on lease
// ownership when the store supports it (core.OwnedCompleter) — so a
// worker that lost its lease can't clobber the new owner's run. Falls
// back to a plain Complete for stores without the extension.
func (w *Worker) completeNode(ctx context.Context, jobID string, status core.JobStatus, result *core.Result) error {
	if oc, ok := w.store.(core.OwnedCompleter); ok {
		return oc.CompleteOwned(ctx, jobID, w.cfg.ID, status, result)
	}
	return w.store.Complete(ctx, jobID, status, result)
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
	// Manifest-only lookup for the retry policy; scope to the graph's tenant so
	// a per-tenant / pinned module resolves to the right version.
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

	// A module may override the worker-global attempt cap via its
	// manifest (e.g. a flaky network module tolerating more retries, or a
	// costly module limiting itself to one shot). Zero = use the default.
	attemptCap := w.cfg.MaxRetries
	if manifest.MaxRetries > 0 {
		attemptCap = manifest.MaxRetries
	}
	if rec.Attempt >= attemptCap {
		return time.Time{}, fmt.Sprintf("max retries (%d) reached", attemptCap)
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
	// Per-node wall-time cap. The context deadline reaches every
	// well-behaved Execute (engine.RunNode passes it through to the
	// transport, which passes it through to http.NewRequestWithContext,
	// sandbox exec, etc.). Modules that ignore ctx will exceed the
	// timeout — we still surface "timeout" as the failure code below
	// so the dispatcher's failure-propagation rules can react cleanly.
	// Every node gets a wall-time cap: its explicit TimeoutSeconds when
	// set, otherwise the worker's DefaultNodeTimeout backstop. The deadline
	// reaches every cancellation-honoring Execute (engine.RunNode →
	// transport → http.NewRequestWithContext / sandbox exec / gRPC stream),
	// so a node blocked on a stalled backend returns instead of holding its
	// slot indefinitely. We surface "timeout" below so the dispatcher's
	// failure-propagation rules react cleanly.
	timeout := w.cfg.DefaultNodeTimeout
	if node, ok := graph.Node(rec.NodeID); ok && node.TimeoutSeconds > 0 {
		timeout = time.Duration(node.TimeoutSeconds) * time.Second
	}
	execCtx := ctx
	var cancelDeadline context.CancelFunc
	if timeout > 0 {
		execCtx, cancelDeadline = context.WithTimeout(ctx, timeout)
		defer cancelDeadline()
	}
	// Loop body: when this node is a for_each whose `body` pin is wired, hand
	// the drop a runner that executes the body subgraph in-process once per
	// item (the drop owns iteration; the engine owns one body pass). The body
	// nodes are already excluded from normal dispatch (see loopBodyOwners).
	if node, ok := graph.Node(rec.NodeID); ok && node.Module == "for_each" {
		if body, isLoop := extractLoopBody(graph, rec.NodeID); isLoop {
			execCtx = engine.WithBodyRunner(execCtx, w.bodyRunner(body, rec.GraphRunID))
		}
	}
	result, err := w.engine.RunNode(execCtx, graph, rec.GraphRunID, rec.NodeID, prior, nodeProgress)
	close(nodeProgress)
	<-forwardDone

	// Translate a deadline expiry into a structured failure. Without
	// this, ctx.Err() bubbling up as a generic error makes per-node
	// timeouts indistinguishable from a network blip in dashboards.
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

// bodyRunner builds the engine.BodyRunner the for_each drop calls once per
// item. Each call runs the body subgraph fully in-process via Engine.Run
// with the item on the context so the body nodes' ${item.…} params resolve
// to that row.
//
// The drop may call the runner from several goroutines at once (concurrency
// > 1). Engine.RunNode resolves a node's params IN PLACE, so every call gets
// a fresh deep copy of the body graph — otherwise concurrent iterations
// would race on (and clobber) the shared node Params, and every row would
// see the last row's values. The clone is a JSON round-trip, which is exact
// because the graph already round-trips through JSON as its stored payload.
func (w *Worker) bodyRunner(body core.Graph, graphRunID string) engine.BodyRunner {
	bodyJSON, marshalErr := json.Marshal(body)
	return func(ctx context.Context, item core.Ref) (engine.GraphResult, error) {
		if marshalErr != nil {
			return engine.GraphResult{}, fmt.Errorf("clone loop body: %w", marshalErr)
		}
		var g core.Graph
		if err := json.Unmarshal(bodyJSON, &g); err != nil {
			return engine.GraphResult{}, fmt.Errorf("clone loop body: %w", err)
		}
		// The parent run's ID rides along so body nodes get the parent's
		// per-run scratch space (file-writing drops like sheets_export_pdf
		// work inside a loop; the dispatcher reclaims the scratch when the
		// parent run finishes).
		ctx = engine.WithLoopRunID(ctx, graphRunID)
		return w.engine.Run(engine.WithLoopItem(ctx, item.Inline), g, nil)
	}
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

// renewLease keeps the job's lease alive while it runs. A renewal that
// fails with ErrConflict/ErrNotFound means we no longer own the job — the
// lease expired and another worker reclaimed it — so we invoke onLost
// (which fences the execution) and stop. Transient errors (DB blips) are
// logged and retried on the next tick; the lease may still be valid.
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
			// Transient (e.g. DB unreachable): keep trying — a later
			// renew can recover before the lease actually lapses.
			w.cfg.Logger.Printf("[%s] renew %s (transient): %v", w.cfg.ID, jobID, err)
		}
	}
}
