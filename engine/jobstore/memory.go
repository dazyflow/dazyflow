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
	rec.Status = core.JobStatusQueued
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
		if r.Status == core.JobStatusQueued {
			candidates = append(candidates, r)
			continue
		}
		// Running jobs whose lease expired are reclaimable.
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
	if status != core.JobStatusSucceeded && status != core.JobStatusFailed && status != core.JobStatusCancelled {
		return core.ErrConflict
	}
	r.Status = status
	r.Result = result
	r.LeaseUntil = nil
	now := m.clock()
	r.FinishedAt = &now
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
