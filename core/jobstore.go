package core

import (
	"context"
	"errors"
	"time"
)

// JobRecord is the persisted view of a job's lifecycle. The engine writes
// state transitions as nodes start, progress, and finish; schedulers and
// workers read records to claim, retry, and resume work after a crash.
type JobRecord struct {
	ID         string
	GraphID    string
	NodeID     string
	Tenant     string
	Workspace  string
	Status     JobStatus
	Job        Job
	Result     *Result
	EnqueuedAt time.Time
	StartedAt  *time.Time
	FinishedAt *time.Time
	Attempt    int
	LeaseUntil *time.Time
	WorkerID   string
}

type JobStatus string

const (
	JobStatusQueued    JobStatus = "queued"
	JobStatusRunning   JobStatus = "running"
	JobStatusSucceeded JobStatus = "succeeded"
	JobStatusFailed    JobStatus = "failed"
	JobStatusCancelled JobStatus = "cancelled"
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

	// Get returns the current record. Returns ErrNotFound if unknown.
	Get(ctx context.Context, jobID string) (JobRecord, error)

	// ListByGraph returns all records for a graph, newest first.
	ListByGraph(ctx context.Context, graphID string) ([]JobRecord, error)
}

var (
	ErrNoJobs   = errors.New("no jobs available")
	ErrNotFound = errors.New("job not found")
	ErrConflict = errors.New("job state conflict")
)
