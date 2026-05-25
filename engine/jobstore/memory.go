// Package jobstore contains JobStore implementations. The Memory store is
// fully tested and suitable for single-node deployments and tests. The
// Postgres store is the production target — see postgres.go for the
// schema and connection model.
package jobstore

import (
	"context"
	"sort"
	"sync"
	"time"

	"git.sr.ht/~klahr/hazy-flow/core"
)

// Memory is an in-memory JobStore. Concurrency-safe; loses state on
// restart. Useful for single-binary deployments and the engine's tests.
type Memory struct {
	mu      sync.Mutex
	records map[string]*core.JobRecord
	clock   func() time.Time
}

func NewMemory() *Memory {
	return &Memory{
		records: make(map[string]*core.JobRecord),
		clock:   time.Now,
	}
}

func (m *Memory) Enqueue(_ context.Context, rec core.JobRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.records[rec.ID]; exists {
		return core.ErrConflict
	}
	if rec.EnqueuedAt.IsZero() {
		rec.EnqueuedAt = m.clock()
	}
	if rec.Kind == "" {
		rec.Kind = core.JobKindGraph
	}
	// Allow callers (e.g. Service.SubmitGraph creating a graph-record) to
	// override the default queued status. Workers only claim queued
	// node-records; graph-records sit at running for the lifetime of the
	// run.
	if rec.Status == "" {
		rec.Status = core.JobStatusQueued
	}
	rec.Attempt = 0
	copy := rec
	m.records[rec.ID] = &copy
	return nil
}

func (m *Memory) Claim(_ context.Context, worker string, lease time.Duration) (core.JobRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.clock()

	candidates := make([]*core.JobRecord, 0)
	for _, r := range m.records {
		// Workers only handle node-kind jobs. Graph-records are status
		// containers updated by whichever worker finalizes the run.
		if r.Kind != core.JobKindNode {
			continue
		}
		// Honor delayed-retry scheduling.
		if r.AvailableAt != nil && r.AvailableAt.After(now) {
			continue
		}
		if r.Status == core.JobStatusQueued {
			candidates = append(candidates, r)
			continue
		}
		if r.Status == core.JobStatusRunning && r.LeaseUntil != nil && r.LeaseUntil.Before(now) {
			candidates = append(candidates, r)
		}
	}
	if len(candidates) == 0 {
		return core.JobRecord{}, core.ErrNoJobs
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].EnqueuedAt.Before(candidates[j].EnqueuedAt)
	})
	picked := candidates[0]
	picked.Status = core.JobStatusRunning
	picked.WorkerID = worker
	picked.Attempt++
	started := now
	picked.StartedAt = &started
	until := now.Add(lease)
	picked.LeaseUntil = &until
	return *picked, nil
}

func (m *Memory) Requeue(_ context.Context, jobID string, availableAt time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.records[jobID]
	if !ok {
		return core.ErrNotFound
	}
	if core.IsTerminalStatus(r.Status) {
		// Terminal records can't be revived; force callers to pick a new
		// record ID if they want to try again.
		return core.ErrConflict
	}
	r.Status = core.JobStatusQueued
	r.AvailableAt = &availableAt
	r.LeaseUntil = nil
	r.Result = nil
	return nil
}

func (m *Memory) Renew(_ context.Context, jobID, worker string, lease time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.records[jobID]
	if !ok {
		return core.ErrNotFound
	}
	if r.WorkerID != worker || r.Status != core.JobStatusRunning {
		return core.ErrConflict
	}
	until := m.clock().Add(lease)
	r.LeaseUntil = &until
	return nil
}

func (m *Memory) Complete(_ context.Context, jobID string, status core.JobStatus, result *core.Result) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.records[jobID]
	if !ok {
		return core.ErrNotFound
	}
	// Accept terminal statuses (the common case) and JobStatusAwaiting
	// (the pause path — caller will Complete again later to terminate).
	if !core.IsTerminalStatus(status) && status != core.JobStatusAwaiting {
		return core.ErrConflict
	}
	// Idempotent: once a record is terminal, refuse further writes so
	// racing workers can use ErrConflict to detect they were beaten to it.
	if core.IsTerminalStatus(r.Status) {
		return core.ErrConflict
	}
	r.Status = status
	r.Result = result
	r.LeaseUntil = nil
	if status != core.JobStatusAwaiting {
		now := m.clock()
		r.FinishedAt = &now
	}
	return nil
}

func (m *Memory) Get(_ context.Context, jobID string) (core.JobRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.records[jobID]
	if !ok {
		return core.JobRecord{}, core.ErrNotFound
	}
	return *r, nil
}

func (m *Memory) ListGraphRuns(_ context.Context, opts core.ListGraphRunsOpts) ([]core.JobRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []core.JobRecord
	for _, r := range m.records {
		if r.Kind != core.JobKindGraph {
			continue
		}
		if opts.Tenant != "" && r.Tenant != opts.Tenant {
			continue
		}
		if opts.Workspace != "" && r.Workspace != opts.Workspace {
			continue
		}
		if opts.GraphID != "" && r.GraphID != opts.GraphID {
			continue
		}
		if opts.Status != "" && r.Status != opts.Status {
			continue
		}
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].EnqueuedAt.After(out[j].EnqueuedAt)
	})
	if opts.Offset > 0 {
		if opts.Offset >= len(out) {
			return nil, nil
		}
		out = out[opts.Offset:]
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit < len(out) {
		out = out[:limit]
	}
	return out, nil
}

func (m *Memory) ListByGraph(_ context.Context, graphID string) ([]core.JobRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []core.JobRecord
	for _, r := range m.records {
		if r.GraphID == graphID {
			out = append(out, *r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].EnqueuedAt.After(out[j].EnqueuedAt)
	})
	return out, nil
}
