// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"errors"
	"time"
)

// JobRecord is the persisted view of a job's lifecycle. The engine writes
// state transitions as nodes start, progress, and finish; schedulers and
// workers read records to claim, retry, and resume work after a crash.
//
// Records come in two flavors today. Graph-level records (Kind=="graph")
// carry the full graph JSON in GraphPayload — a worker that claims one
// runs the entire graph through Engine.Run. Per-node records exist in the
// schema for the future split where the engine emits one queue entry per
// node and workers dispatch them independently.
type JobRecord struct {
	ID           string
	Kind         JobKind
	GraphRunID   string // empty for graph-records; set on node-records to link them up
	GraphID      string
	NodeID       string
	Tenant       string
	Workspace    string
	Status       JobStatus
	Job          Job
	GraphPayload []byte // JSON-encoded core.Graph; lives on the graph-record
	Result       *Result
	EnqueuedAt   time.Time
	AvailableAt  *time.Time // when non-nil, Claim skips this record until the time passes
	StartedAt    *time.Time
	FinishedAt   *time.Time
	Attempt      int
	LeaseUntil   *time.Time
	WorkerID     string

	// Manual marks a run a person started from the app — the editor's Run
	// button, an inspector node preview, a test trigger, a retry — as opposed
	// to one the scheduler, a webhook or a form started on its own.
	//
	// It exists to answer one question: is anybody looking? A failure email is
	// for a run that failed while nobody was watching. Someone who pressed Run
	// and is watching the canvas light up red does not need to be told by
	// email, and being told anyway trains people to ignore the mail that
	// matters. So a manual run sends no failure email; the per-flow webhook
	// still fires, because that is a machine channel the author wired
	// deliberately.
	//
	// Stored rather than passed around because a run can be PARKED at the
	// concurrency limit and promoted minutes later, in another goroutine with
	// no memory of who started it (see promote.go).
	Manual bool

	// TriggerDepth is how many runs deep the trigger chain that started
	// this run already was — 0 for a run a person or the scheduler started,
	// N for one a previous run's HTTP step set off through this instance's
	// own trigger endpoint. See TriggerDepthHeader; the trigger endpoints
	// refuse past MaxTriggerChainDepth, which is what stops a flow that
	// calls its own trigger URL from running forever.
	TriggerDepth int

	// ParentNodeRecID links a child graph-record back to the parent
	// node-record that submitted it (via the subgraph module). Empty
	// for top-level graph submissions. The dispatcher uses it when the
	// child graph terminates to resume the parent's awaiting record.
	ParentNodeRecID string
}

// Valid reports whether s is a defined job status. Empty is valid and means
// "no status filter / unset".
//
// Job statuses are written by this package and read back from storage, so
// they are not attacker-controlled — but they DO arrive as free strings on
// the list-runs query filter, where an unrecognized value used to produce a
// silently empty result set rather than an error.
func (s JobStatus) Valid() bool {
	switch s {
	case "", JobStatusQueued, JobStatusRunning, JobStatusSucceeded,
		JobStatusFailed, JobStatusCancelled, JobStatusSkipped, JobStatusAwaiting:
		return true
	}
	return false
}

// IsTerminalStatus reports whether s represents a final state — used by
// the JobStore to make Complete idempotent and by callers polling for end.
func IsTerminalStatus(s JobStatus) bool {
	switch s {
	case JobStatusSucceeded, JobStatusFailed, JobStatusCancelled, JobStatusSkipped:
		return true
	}
	return false
}

type JobKind string

const (
	JobKindGraph JobKind = "graph"
	JobKindNode  JobKind = "node"
)

type JobStatus string

const (
	JobStatusQueued    JobStatus = "queued"
	JobStatusRunning   JobStatus = "running"
	JobStatusSucceeded JobStatus = "succeeded"
	JobStatusFailed    JobStatus = "failed"
	JobStatusCancelled JobStatus = "cancelled"
	// JobStatusSkipped marks a node that was intentionally not executed —
	// either because all its incoming fallback edges were dormant
	// (source succeeded) or because a non-tolerated predecessor failure
	// blocked its only data path while a fallback elsewhere kept the
	// graph alive.
	JobStatusSkipped JobStatus = "skipped"
	// JobStatusAwaiting parks a node mid-execution while it waits for
	// an external signal — today only the await_approval module uses
	// it. Awaiting is NOT terminal: graph completion holds, dependents
	// are not dispatched, and the worker is freed. The resume path
	// (daemon.Service.Approve) transitions the record to Succeeded
	// once the external signal arrives.
	JobStatusAwaiting JobStatus = "awaiting"
)

// JobStore is the persistence boundary for jobs. Production deployments
// back it with Postgres (SKIP LOCKED for queueing, advisory locks for
// leader election); tests and single-node setups can use the in-memory
// implementation under engine/jobstore.
type JobStore interface {
	// Enqueue inserts a new queued record. The store assigns EnqueuedAt.
	Enqueue(ctx context.Context, rec JobRecord) error

	// Claim atomically grabs the next queued job for worker, extending a
	// lease until now+lease. Returns ErrNoJobs if nothing is available.
	Claim(ctx context.Context, worker string, lease time.Duration) (JobRecord, error)

	// Renew extends the lease on a job already held by worker.
	Renew(ctx context.Context, jobID, worker string, lease time.Duration) error

	// Complete writes a terminal state. Status must be one of succeeded,
	// failed, or cancelled.
	Complete(ctx context.Context, jobID string, status JobStatus, result *Result) error

	// Requeue moves a record back to queued state with an availability
	// horizon. Workers re-pick it once AvailableAt passes. Used to
	// implement retry-with-backoff. The record's Attempt counter and
	// history are preserved; Result is cleared.
	Requeue(ctx context.Context, jobID string, availableAt time.Time) error

	// Get returns the current record. Returns ErrNotFound if unknown.
	Get(ctx context.Context, jobID string) (JobRecord, error)

	// ListByGraph returns all records for a graph, newest first.
	ListByGraph(ctx context.Context, graphID string) ([]JobRecord, error)

	// ListGraphRuns returns only graph-kind records (the runs, not the
	// per-node records) matching the supplied scope. Used by the UI's
	// per-graph history and the workspace-wide runs view. Sorted by
	// EnqueuedAt DESC; pagination via Limit + Offset.
	ListGraphRuns(ctx context.Context, opts ListGraphRunsOpts) ([]JobRecord, error)

	// ListNodeRecords returns only node-kind records matching the
	// supplied scope. Used by the approval inbox (Status=awaiting) and
	// future "all failed nodes" / "all running nodes" views. Sorted by
	// EnqueuedAt DESC; pagination via Limit + Offset.
	ListNodeRecords(ctx context.Context, opts ListNodeRecordsOpts) ([]JobRecord, error)
}

// OwnedCompleter is an optional JobStore extension that fences a Complete
// on lease ownership: the write only lands if worker still holds the
// job's lease. The worker uses it for node terminal/awaiting writes so a
// worker that lost its lease (reclaimed elsewhere) can't clobber the new
// owner's run — it gets ErrConflict and abandons instead. Non-lease
// callers (dispatcher graph-records, cancel) keep using plain Complete.
type OwnedCompleter interface {
	CompleteOwned(ctx context.Context, jobID, worker string, status JobStatus, result *Result) error
}

// NodeOutcome is the slice of a node record a DEPENDENT needs: whether the
// step produced data, and what it produced. Everything else on the record —
// the Job with its params, input refs and env, the lease and timing columns —
// is dead weight on that read.
type NodeOutcome struct {
	Status JobStatus
	Result *Result
}

// OutcomeReader is an optional JobStore extension that reads the outcomes of
// several node records in ONE round trip. A step assembles its input from
// every predecessor that feeds it, and reading those one full record at a
// time costs a query per incoming edge plus a decode of each predecessor's
// Job JSON, which the dependent never looks at. Keys of the returned map are
// the job IDs that exist; a missing ID is absent rather than an error, so one
// call reports both.
type OutcomeReader interface {
	Outcomes(ctx context.Context, jobIDs []string) (map[string]NodeOutcome, error)
}

// CompleteEnqueuer is an optional JobStore extension that writes a node's
// completion and queues the dependents it released as ONE transaction.
// Done separately they are two commits per step — the dominant cost on the
// execution path — and leave a window where the node is finished but its
// successor is not yet queued, which nothing but a stuck-run sweep closes.
//
// Semantics match CompleteOwned followed by Enqueue of each dependent: the
// write is fenced on worker's lease when worker is set, ErrConflict means
// nothing was written, and a dependent that already exists is left alone
// and not counted. Dependents are also not queued when the run's own record
// is already terminal (cancelled), so a node finishing mid-cancel can't
// revive the run; RunStatus reports that state as of the write.
type CompleteEnqueuer interface {
	CompleteAndEnqueue(ctx context.Context, jobID, worker string, status JobStatus, result *Result, dependents []JobRecord) (Advance, error)
}

// Advance is what CompleteAndEnqueue saw and did.
type Advance struct {
	// RunStatus is the graph-run record's status at the time of the write,
	// or "" when the node has no run record.
	RunStatus JobStatus
	// Enqueued counts dependents newly queued; pre-existing ones don't count.
	Enqueued int
}

// JobCounter is an optional JobStore extension exposing aggregate
// node-job counts for metrics — queue depth (queued) and in-flight
// (running) are the load-bearing signals. Implemented by the Memory and
// Postgres stores; the metrics endpoint type-asserts to it.
type JobCounter interface {
	// CountsByStatus returns the number of node-kind job records in each
	// status, as a point-in-time aggregate.
	CountsByStatus(ctx context.Context) (map[JobStatus]int, error)
}

// GraphRunStarter is an optional JobStore extension: flip a graph-kind record
// from queued (held pending by the per-tenant concurrency admission queue) to
// running. Returns true only when THIS call performed the transition (the row
// was queued); false means it was already running/terminal — another promoter
// won the race. The conditional update is the concurrency control, so the
// promoter needs no external lock. Implemented by the Memory and Postgres
// stores.
type GraphRunStarter interface {
	MarkGraphRunning(ctx context.Context, jobID string) (bool, error)
}

// GraphRunParker is an optional JobStore extension: flip a graph-kind record
// between running and awaiting as the run parks on, and resumes from, a human
// approval. Returns true only when THIS call performed the transition; false
// means the record was already in the target state, is terminal, or doesn't
// exist. Both directions are conditional updates, which is what makes them
// safe to call from every park and every resume without counting: a run with
// two steps parked at once takes the first park's transition and no-ops the
// second, and the last resume takes it back.
//
// This is what makes "waiting for approval" a real run status rather than a
// property you have to open the run to discover — the runs list filters on it,
// and a parked run stops claiming to be Running while it sits there for a day.
// It is deliberately NOT set for the other kind of pause (a subgraph node
// waiting on its child): that run has work in flight and nothing to decide.
//
// Implemented by the Memory and Postgres stores.
type GraphRunParker interface {
	// SetGraphRunParked moves a graph record running → awaiting when parked
	// is true, and awaiting → running when it is false.
	SetGraphRunParked(ctx context.Context, graphRunID string, parked bool) (bool, error)
}

// ListNodeRecordsOpts scopes a ListNodeRecords call. Same shape as
// ListGraphRunsOpts but for the node-kind half of the table.
//
// GraphRunID, when set, narrows to a single run's node records —
// used by the run-detail UI to draw a per-node timeline without
// fetching nodes by ID one at a time.
type ListNodeRecordsOpts struct {
	Tenant     string
	Workspace  string
	Status     JobStatus
	GraphRunID string
	Limit      int
	Offset     int

	// GraphID narrows to the node records of ONE flow, across all of its
	// runs. GraphRunID answers "what happened in this run"; this answers
	// "what has this flow's steps produced lately", which is what the editor
	// needs to show a step's last output without a run id to hand. Backed by
	// jobs_graph_idx (graph_id, enqueued_at DESC), so the newest-first order
	// the callers want is the index's own.
	GraphID string

	// HasOutputPort narrows to records whose Result carries this output
	// port, letting a caller ask for a KIND of node without a module column
	// to filter on. The approval views use it: an await_approval node is the
	// one that emits `pending_url`, and that port survives the resume, so it
	// identifies the node both while it is parked and after it is decided.
	//
	// The filter belongs in the store rather than in a Go loop over the
	// results, because the two are not the same query. Filtering after a
	// LIMIT asks for "the newest 100 nodes, of which show me the approvals"
	// — on a workspace where approvals are a rounding error against ordinary
	// succeeded steps, that reliably returns an empty page while history
	// exists.
	HasOutputPort string

	// NewestByFinished orders by finish time instead of enqueue time (both
	// DESC). An approval's enqueue time is when the flow reached the step;
	// its finish time is when a person decided. A history list is ordered by
	// the decision, so a request parked for three weeks and approved this
	// morning belongs at the top — and with a LIMIT, ordering also decides
	// which rows are returned at all, not just their order. Records with no
	// finish time sort last.
	NewestByFinished bool
}

// ListGraphRunsOpts scopes a ListGraphRuns call. Empty fields are
// wildcards within their layer — Tenant is required, Workspace and
// GraphID are optional. Status, when set, filters to that single
// status. Limit=0 means "use the store's default" (typically 50).
type ListGraphRunsOpts struct {
	Tenant    string
	Workspace string
	GraphID   string
	Status    JobStatus
	// Since and Until bound a run's enqueue time (EnqueuedAt). Since is
	// inclusive (enqueued_at >= Since) and Until is exclusive (enqueued_at <
	// Until), so a caller passing one day's midnight as Since and the next
	// day's midnight as Until gets exactly that day with no boundary
	// double-count. A zero value leaves that side of the range unbounded.
	Since  time.Time
	Until  time.Time
	Limit  int
	Offset int
}

// RunSummary is the slice of a graph-run record that the list views, the
// promoter and the admission check actually read: identity, status and
// timing. The rest of the record is dead weight on those reads, and one
// field of it is enormous — GraphPayload holds the whole flow JSON the run
// pinned at submit, tens of kilobytes per row, TOASTed and compressed in
// Postgres. A run list is polled every couple of seconds by every open tab,
// so it was transferring and decompressing megabytes to render seven scalars.
type RunSummary struct {
	ID         string
	GraphID    string
	Tenant     string
	Workspace  string
	Status     JobStatus
	EnqueuedAt time.Time
	StartedAt  *time.Time
	FinishedAt *time.Time
	// Error is the run's failure, projected out of the stored Result so the
	// Result itself — which also holds every output value the run produced —
	// never has to be fetched or decoded.
	Error *JobError
}

// ErrorCode is the failure's code, or "" for a run that did not fail.
func (s RunSummary) ErrorCode() string {
	if s.Error == nil {
		return ""
	}
	return s.Error.Code
}

// RunSummaryReader is an optional JobStore extension that reads graph runs
// without their payloads, and counts them without materializing rows at all.
// Same scoping and ordering as ListGraphRuns — newest-first, Limit/Offset —
// so a caller can swap one for the other and see the same runs.
//
// CountGraphRuns honours Limit as a ceiling, because its callers only ask
// whether a bound is reached: counting past it is work with no reader.
// Limit <= 0 counts every match.
type RunSummaryReader interface {
	ListGraphRunSummaries(ctx context.Context, opts ListGraphRunsOpts) ([]RunSummary, error)
	CountGraphRuns(ctx context.Context, opts ListGraphRunsOpts) (int, error)
	// GetGraphRunSummary is the same projection for one run by id, for the
	// run-detail header — which the browser re-polls while the run is live
	// and which shows nothing the payload could answer. ErrNotFound when
	// there is no such record.
	GetGraphRunSummary(ctx context.Context, jobID string) (RunSummary, error)
}

// SummarizeRun projects a full record onto the summary the readers above
// return. It is the fallback path's definition of the projection, and so
// also the specification the store implementations are checked against.
func SummarizeRun(rec JobRecord) RunSummary {
	s := RunSummary{
		ID: rec.ID, GraphID: rec.GraphID, Tenant: rec.Tenant, Workspace: rec.Workspace,
		Status: rec.Status, EnqueuedAt: rec.EnqueuedAt,
		StartedAt: rec.StartedAt, FinishedAt: rec.FinishedAt,
	}
	if rec.Result != nil {
		s.Error = rec.Result.Error
	}
	return s
}

// NodeRun is the slice of a node record the run viewer's timeline renders:
// which step it was, how it went, when, and the values it consumed and
// produced. Everything else on the record belongs to the queue rather than to
// the view — the ids that link it to its run, the tenant and workspace the run
// was already scoped by, the lease and worker that claimed it — and the run
// viewer re-reads this list every couple of seconds while a run is live, once
// per open tab, so carrying them is the same waste RunSummary removed a layer
// up.
type NodeRun struct {
	NodeID     string
	Status     JobStatus
	Attempt    int
	StartedAt  *time.Time
	FinishedAt *time.Time
	// AvailableAt is the retry horizon: a queued node with attempts behind it
	// and a future availability is between automatic attempts, which the view
	// reports as "retrying" rather than as stuck.
	AvailableAt *time.Time
	// Inputs is what the node received. Written on the record's job, not
	// derived from the result — a node that has not finished still has them.
	Inputs map[string]Ref
	Result *Result
}

// SummarizeNodeRun projects a full record onto the node run above. It is the
// fallback path's definition of the projection, and so also the specification
// the store implementations are checked against.
func SummarizeNodeRun(rec JobRecord) NodeRun {
	return NodeRun{
		NodeID: rec.NodeID, Status: rec.Status, Attempt: rec.Attempt,
		StartedAt: rec.StartedAt, FinishedAt: rec.FinishedAt,
		AvailableAt: rec.AvailableAt, Inputs: rec.Job.Input, Result: rec.Result,
	}
}

// NodeRunReader is an optional JobStore extension that reads one run's node
// records in the shape above. Same scoping and ordering as ListNodeRecords
// for the same run — newest-first — so a caller can swap one for the other
// and see the same steps in the same order.
type NodeRunReader interface {
	ListNodeRuns(ctx context.Context, graphRunID string, limit int) ([]NodeRun, error)
}

// ListNodeRuns reads one run's node timeline, using the store's narrow
// projection when it has one and falling back to a full ListNodeRecords
// otherwise. Callers use this rather than type-asserting themselves, so a
// store without the extension stays correct rather than unsupported.
func ListNodeRuns(ctx context.Context, store JobStore, graphRunID string, limit int) ([]NodeRun, error) {
	if r, ok := store.(NodeRunReader); ok {
		return r.ListNodeRuns(ctx, graphRunID, limit)
	}
	recs, err := store.ListNodeRecords(ctx, ListNodeRecordsOpts{GraphRunID: graphRunID, Limit: limit})
	if err != nil {
		return nil, err
	}
	out := make([]NodeRun, 0, len(recs))
	for _, rec := range recs {
		out = append(out, SummarizeNodeRun(rec))
	}
	return out, nil
}

// ListRunSummaries reads a page of run summaries, using the store's narrow
// projection when it has one and falling back to a full ListGraphRuns
// otherwise. Callers use this rather than type-asserting themselves, so a
// store without the extension stays correct rather than unsupported.
func ListRunSummaries(ctx context.Context, store JobStore, opts ListGraphRunsOpts) ([]RunSummary, error) {
	if r, ok := store.(RunSummaryReader); ok {
		return r.ListGraphRunSummaries(ctx, opts)
	}
	recs, err := store.ListGraphRuns(ctx, opts)
	if err != nil {
		return nil, err
	}
	out := make([]RunSummary, 0, len(recs))
	for _, rec := range recs {
		out = append(out, SummarizeRun(rec))
	}
	return out, nil
}

// GetRunSummary reads one run's summary. It is exactly SummarizeRun of what
// Get returns for the same id — including for an id that is not a run's, so
// that swapping a caller from Get to this one cannot change which ids it
// accepts. Same fallback arrangement as ListRunSummaries.
func GetRunSummary(ctx context.Context, store JobStore, jobID string) (RunSummary, error) {
	if r, ok := store.(RunSummaryReader); ok {
		return r.GetGraphRunSummary(ctx, jobID)
	}
	rec, err := store.Get(ctx, jobID)
	if err != nil {
		return RunSummary{}, err
	}
	return SummarizeRun(rec), nil
}

// CountRuns counts the runs matching opts, capped at opts.Limit when that is
// positive. Same fallback arrangement as ListRunSummaries — but the fallback
// has to PAGE, because ListGraphRuns imposes a default page size on an unset
// Limit, and returning that page's length would report "50" for a tenant with
// thousands of runs.
func CountRuns(ctx context.Context, store JobStore, opts ListGraphRunsOpts) (int, error) {
	if r, ok := store.(RunSummaryReader); ok {
		return r.CountGraphRuns(ctx, opts)
	}
	const page = 200
	ceiling := opts.Limit
	opts.Limit = page
	total := 0
	for {
		if ceiling > 0 && ceiling-total < page {
			opts.Limit = ceiling - total
		}
		recs, err := store.ListGraphRuns(ctx, opts)
		if err != nil {
			return 0, err
		}
		total += len(recs)
		if len(recs) < opts.Limit || (ceiling > 0 && total >= ceiling) {
			return total, nil
		}
		opts.Offset += len(recs)
	}
}

var (
	ErrNoJobs   = errors.New("no jobs available")
	ErrNotFound = errors.New("job not found")
	ErrConflict = errors.New("job state conflict")
)
