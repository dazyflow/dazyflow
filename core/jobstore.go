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

	// ParentNodeRecID links a child graph-record back to the parent
	// node-record that submitted it (via the subgraph module). Empty
	// for top-level graph submissions. The dispatcher uses it when the
	// child graph terminates to resume the parent's awaiting record.
	ParentNodeRecID string
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

// ListNodeRecordsOpts scopes a ListNodeRecords call. Same shape as
// ListGraphRunsOpts but for the node-kind half of the table.
type ListNodeRecordsOpts struct {
	Tenant    string
	Workspace string
	Status    JobStatus
	Limit     int
	Offset    int
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
	Limit     int
	Offset    int
}

var (
	ErrNoJobs   = errors.New("no jobs available")
	ErrNotFound = errors.New("job not found")
	ErrConflict = errors.New("job state conflict")
)
