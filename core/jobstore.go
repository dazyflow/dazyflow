// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"errors"
	"time"
)

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

	// Manual suppresses the failure email: someone watching the canvas go red does
	// not need one, and sending it anyway trains people to ignore the mail that
	// matters. The per-flow webhook still fires. Stored rather than passed around
	// because a run can be parked at the concurrency limit and promoted minutes
	// later, in another goroutine with no memory of who started it.
	Manual bool

	TriggerDepth int

	ParentNodeRecID string
}

func (s JobStatus) Valid() bool {
	switch s {
	case "", JobStatusQueued, JobStatusRunning, JobStatusSucceeded,
		JobStatusFailed, JobStatusCancelled, JobStatusSkipped, JobStatusAwaiting:
		return true
	}
	return false
}

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
	JobStatusSkipped   JobStatus = "skipped"
	// NOT terminal: graph completion holds and dependents are not dispatched, but
	// the worker is freed.
	JobStatusAwaiting JobStatus = "awaiting"
)

type JobStore interface {
	Enqueue(ctx context.Context, rec JobRecord) error

	Claim(ctx context.Context, worker string, lease time.Duration) (JobRecord, error)

	Renew(ctx context.Context, jobID, worker string, lease time.Duration) error

	Complete(ctx context.Context, jobID string, status JobStatus, result *Result) error

	Requeue(ctx context.Context, jobID string, availableAt time.Time) error

	Get(ctx context.Context, jobID string) (JobRecord, error)

	ListByGraph(ctx context.Context, graphID string) ([]JobRecord, error)

	ListGraphRuns(ctx context.Context, opts ListGraphRunsOpts) ([]JobRecord, error)

	ListNodeRecords(ctx context.Context, opts ListNodeRecordsOpts) ([]JobRecord, error)
}

type FailureNotifier interface {
	ClaimUnnotified(ctx context.Context, lookback time.Duration, maxAttempts, limit int) ([]JobRecord, error)

	ReleaseNotifyClaim(ctx context.Context, jobID string) error
}

type OwnedCompleter interface {
	CompleteOwned(ctx context.Context, jobID, worker string, status JobStatus, result *Result) error
}

type NodeOutcome struct {
	Status JobStatus
	Result *Result
}

type OutcomeReader interface {
	Outcomes(ctx context.Context, jobIDs []string) (map[string]NodeOutcome, error)
}

// One transaction, so there is no window with the node finished and its successor
// not yet queued. ErrConflict means nothing was written; an existing dependent is
// skipped, and none are queued when the run record is already terminal, so a node
// finishing mid-cancel cannot revive the run.
type CompleteEnqueuer interface {
	CompleteAndEnqueue(ctx context.Context, jobID, worker string, status JobStatus, result *Result, dependents []JobRecord) (Advance, error)
}

type Advance struct {
	RunStatus JobStatus
	Enqueued  int
}

// ALL OR NOTHING, which is what makes it safe to try first: on any error nothing
// was written, so the caller can fall back to one-at-a-time and get exactly the
// per-record errors it had before. Records must share a tenant.
type NodeBatchEnqueuer interface {
	EnqueueNodes(ctx context.Context, recs []JobRecord) (int, error)
}

func BatchableNode(rec JobRecord) bool {
	return rec.Kind == JobKindNode &&
		(rec.Status == "" || rec.Status == JobStatusQueued) &&
		rec.Result == nil && len(rec.GraphPayload) == 0 &&
		rec.ParentNodeRecID == "" && !rec.Manual &&
		rec.EnqueuedAt.IsZero() && rec.AvailableAt == nil &&
		rec.StartedAt == nil && rec.FinishedAt == nil &&
		rec.LeaseUntil == nil && rec.WorkerID == "" && rec.Attempt == 0
}

// When ok is false NOTHING was written, so the caller must enqueue one at a time
// — that fallback is the only path reporting WHICH record failed, and callers
// differ on what they do about it.
func TryEnqueueNodes(ctx context.Context, store JobStore, recs []JobRecord) (int, bool) {
	if len(recs) < 2 {
		return 0, false // one record is already one write
	}
	b, okStore := store.(NodeBatchEnqueuer)
	if !okStore {
		return 0, false
	}
	for _, r := range recs {
		if !BatchableNode(r) || r.Tenant != recs[0].Tenant {
			return 0, false
		}
	}
	n, err := b.EnqueueNodes(ctx, recs)
	if err != nil {
		return 0, false
	}
	return n, true
}

type JobCounter interface {
	CountsByStatus(ctx context.Context) (map[JobStatus]int, error)
}

// The conditional update IS the concurrency control, so the promoter needs no
// external lock.
type GraphRunStarter interface {
	MarkGraphRunning(ctx context.Context, jobID string) (bool, error)
}

// Both directions are conditional, so a run with two steps parked at once takes
// the first park and no-ops the second. Deliberately NOT set for a subgraph node
// waiting on its child: that run has work in flight and nothing to decide.
type GraphRunParker interface {
	SetGraphRunParked(ctx context.Context, graphRunID string, parked bool) (bool, error)
}

type ListNodeRecordsOpts struct {
	Tenant     string
	Workspace  string
	Status     JobStatus
	GraphRunID string
	Limit      int
	Offset     int

	// Backed by jobs_graph_idx, so newest-first is the index's own order.
	GraphID string

	HasOutputPort string

	NewestByFinished bool
}

type ListGraphRunsOpts struct {
	Tenant    string
	Workspace string
	GraphID   string
	Status    JobStatus
	// Since inclusive, Until exclusive, so one midnight to the next is exactly that
	// day with no boundary double-count.
	Since  time.Time
	Until  time.Time
	Limit  int
	Offset int
}

type RunSummary struct {
	ID         string
	GraphID    string
	Tenant     string
	Workspace  string
	Status     JobStatus
	EnqueuedAt time.Time
	StartedAt  *time.Time
	FinishedAt *time.Time
	Error      *JobError
}

func (s RunSummary) ErrorCode() string {
	if s.Error == nil {
		return ""
	}
	return s.Error.Code
}

type RunSummaryReader interface {
	ListGraphRunSummaries(ctx context.Context, opts ListGraphRunsOpts) ([]RunSummary, error)
	CountGraphRuns(ctx context.Context, opts ListGraphRunsOpts) (int, error)
	GetGraphRunSummary(ctx context.Context, jobID string) (RunSummary, error)
}

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

type NodeRun struct {
	NodeID      string
	Status      JobStatus
	Attempt     int
	StartedAt   *time.Time
	FinishedAt  *time.Time
	AvailableAt *time.Time
	Inputs      map[string]Ref
	Result      *Result
}

func SummarizeNodeRun(rec JobRecord) NodeRun {
	return NodeRun{
		NodeID: rec.NodeID, Status: rec.Status, Attempt: rec.Attempt,
		StartedAt: rec.StartedAt, FinishedAt: rec.FinishedAt,
		AvailableAt: rec.AvailableAt, Inputs: rec.Job.Input, Result: rec.Result,
	}
}

type NodeRunReader interface {
	ListNodeRuns(ctx context.Context, graphRunID string, limit int) ([]NodeRun, error)
	// Limit is a CEILING, and load-bearing: the list this badge counts is capped at
	// the same Limit, so counting past it would claim a number the inbox never shows.
	CountNodeRecords(ctx context.Context, opts ListNodeRecordsOpts) (int, error)
}

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

// Accepts exactly the ids Get accepts, including ones that are not a run's, so
// swapping a caller from Get cannot change which ids work.
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

// The fallback must PAGE: ListGraphRuns imposes a default page size on an unset
// Limit, so returning that page's length reports "50" for a tenant with
// thousands.
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

func CountNodeRecords(ctx context.Context, store JobStore, opts ListNodeRecordsOpts) (int, error) {
	if r, ok := store.(NodeRunReader); ok {
		return r.CountNodeRecords(ctx, opts)
	}
	const page = 200
	ceiling := opts.Limit
	opts.Limit = page
	total := 0
	for {
		if ceiling > 0 && ceiling-total < page {
			opts.Limit = ceiling - total
		}
		recs, err := store.ListNodeRecords(ctx, opts)
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
