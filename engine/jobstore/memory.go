// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package jobstore contains JobStore implementations. The Memory store is
// fully tested and suitable for single-node deployments and tests. The
// Postgres store is the production target — see postgres.go for the
// schema and connection model.
package jobstore

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/dazyflow/dazyflow/core"
)

// Memory is an in-memory JobStore. Concurrency-safe; loses state on
// restart. Useful for single-binary deployments and the engine's tests.
type Memory struct {
	mu            sync.Mutex
	records       map[string]*core.JobRecord
	clock         func() time.Time
	maxConcurrent int // per-tenant running-node cap; 0 = unlimited
}

func NewMemory() *Memory {
	return &Memory{
		records: make(map[string]*core.JobRecord),
		clock:   time.Now,
	}
}

// DeleteByTenant hard-deletes every job record owned by a tenant (GDPR
// erasure cascade, Art. 17). Mirrors the Postgres store. Returns the count.
func (m *Memory) DeleteByTenant(_ context.Context, tenant string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for id, r := range m.records {
		if r.Tenant == tenant {
			delete(m.records, id)
			n++
		}
	}
	return n, nil
}

// SetMaxConcurrentPerTenant caps how many node jobs a single tenant may
// have running at once. Claim won't hand out new (queued) work to a
// tenant already at the cap; reclaiming a node whose lease expired is
// exempt (it's recovery of existing work, not new concurrency). 0 = no
// cap. Set once at startup.
func (m *Memory) SetMaxConcurrentPerTenant(n int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.maxConcurrent = n
}

func (m *Memory) Enqueue(_ context.Context, rec core.JobRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.enqueueLocked(rec)
}

func (m *Memory) enqueueLocked(rec core.JobRecord) error {
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
	// A record enqueued already-terminal (a seeded webhook/trigger node)
	// or already-running (a graph-record) never passes through Claim, so
	// stamp its start — and for terminal seeds the finish — here. Mirrors
	// the Postgres store, and keeps run durations renderable.
	if core.IsTerminalStatus(rec.Status) || rec.Status == core.JobStatusRunning {
		now := m.clock()
		if rec.StartedAt == nil {
			rec.StartedAt = &now
		}
		if rec.FinishedAt == nil && core.IsTerminalStatus(rec.Status) {
			rec.FinishedAt = &now
		}
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

	// Per-tenant concurrency cap: tally tenants' live-running node jobs
	// (lease not expired) so we can withhold new queued work from any
	// tenant already at the cap. Expired-lease "running" rows are dead
	// work being recovered, so they don't count toward the live total.
	var runningByTenant map[string]int
	if m.maxConcurrent > 0 {
		runningByTenant = make(map[string]int)
		for _, r := range m.records {
			if r.Kind == core.JobKindNode && r.Status == core.JobStatusRunning &&
				r.LeaseUntil != nil && r.LeaseUntil.After(now) {
				runningByTenant[r.Tenant]++
			}
		}
	}

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
			// Withhold new work from tenants at their concurrency cap.
			if m.maxConcurrent > 0 && runningByTenant[r.Tenant] >= m.maxConcurrent {
				continue
			}
			candidates = append(candidates, r)
			continue
		}
		if r.Status == core.JobStatusRunning && r.LeaseUntil != nil && r.LeaseUntil.Before(now) {
			// Reclaiming an expired lease is recovery, not new
			// concurrency — exempt from the cap.
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

// CountsByStatus implements core.JobCounter: a tally of node-kind job
// records by status (graph-kind container records are excluded).
func (m *Memory) CountsByStatus(_ context.Context) (map[core.JobStatus]int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[core.JobStatus]int)
	for _, r := range m.records {
		if r.Kind == core.JobKindNode {
			out[r.Status]++
		}
	}
	return out, nil
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
	return m.complete(jobID, "", status, result)
}

// CompleteOwned implements core.OwnedCompleter: Complete, but only if
// worker still owns the record (ErrConflict otherwise).
func (m *Memory) CompleteOwned(_ context.Context, jobID, worker string, status core.JobStatus, result *core.Result) error {
	return m.complete(jobID, worker, status, result)
}

// CompleteAndEnqueue implements core.CompleteEnqueuer under one lock hold,
// which is this store's transaction.
func (m *Memory) CompleteAndEnqueue(_ context.Context, jobID, worker string, status core.JobStatus, result *core.Result, deps []core.JobRecord) (core.Advance, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.completeLocked(jobID, worker, status, result); err != nil {
		return core.Advance{}, err
	}
	var adv core.Advance
	if run, ok := m.records[m.records[jobID].GraphRunID]; ok {
		adv.RunStatus = run.Status
		if core.IsTerminalStatus(run.Status) {
			return adv, nil
		}
	}
	for _, d := range deps {
		d.Kind = core.JobKindNode
		d.Status = core.JobStatusQueued
		switch err := m.enqueueLocked(d); {
		case err == nil:
			adv.Enqueued++
		case errors.Is(err, core.ErrConflict):
		default:
			return adv, err
		}
	}
	return adv, nil
}

// complete is the shared body. worker == "" skips the ownership check
// (the plain Complete used by non-lease callers).
func (m *Memory) complete(jobID, worker string, status core.JobStatus, result *core.Result) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.completeLocked(jobID, worker, status, result)
}

func (m *Memory) completeLocked(jobID, worker string, status core.JobStatus, result *core.Result) error {
	r, ok := m.records[jobID]
	if !ok {
		return core.ErrNotFound
	}
	// Ownership fence: a worker that lost its lease (record reclaimed by
	// another worker) must not be able to write a result.
	if worker != "" && r.WorkerID != worker {
		return core.ErrConflict
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
	// Same for a re-park: awaiting → awaiting means a node that already
	// parked executed a second time (expired lease reclaimed mid-run), and
	// letting the write through announced one pause twice — which the
	// daemon's park hook turned into a duplicate approval email. Mirrors the
	// Postgres guard; the two stores must agree on ErrConflict here.
	if status == core.JobStatusAwaiting && r.Status == core.JobStatusAwaiting {
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

// SetGraphRunParked implements core.GraphRunParker. Mirrors the Postgres
// conditional update: only the transition out of the expected status counts,
// so repeat parks and non-final resumes are no-ops, and a terminal record is
// never revived.
func (m *Memory) SetGraphRunParked(_ context.Context, graphRunID string, parked bool) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.records[graphRunID]
	if !ok {
		return false, core.ErrNotFound
	}
	from, to := core.JobStatusRunning, core.JobStatusAwaiting
	if !parked {
		from, to = core.JobStatusAwaiting, core.JobStatusRunning
	}
	if r.Kind != core.JobKindGraph || r.Status != from {
		return false, nil
	}
	r.Status = to
	return true, nil
}

// MarkGraphRunning implements core.GraphRunStarter: flip a pending (queued)
// graph record to running. Returns true only when this call performed the
// transition (mirrors the Postgres conditional UPDATE).
func (m *Memory) MarkGraphRunning(_ context.Context, jobID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.records[jobID]
	if !ok {
		return false, core.ErrNotFound
	}
	if r.Kind != core.JobKindGraph || r.Status != core.JobStatusQueued {
		return false, nil
	}
	r.Status = core.JobStatusRunning
	if r.StartedAt == nil {
		now := m.clock()
		r.StartedAt = &now
	}
	return true, nil
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
		// Since is inclusive, Until exclusive — mirrors the Postgres store's
		// enqueued_at >= Since AND enqueued_at < Until predicates.
		if !opts.Since.IsZero() && r.EnqueuedAt.Before(opts.Since) {
			continue
		}
		if !opts.Until.IsZero() && !r.EnqueuedAt.Before(opts.Until) {
			continue
		}
		out = append(out, *r)
	}
	// Match the Postgres store's "enqueued_at DESC, id DESC": id breaks
	// enqueued_at ties so pagination is a stable total order (plain sort.Slice
	// isn't even stable), and a tie on a page boundary can't repeat or drop a
	// row across LIMIT/OFFSET pages.
	sort.Slice(out, func(i, j int) bool {
		if out[i].EnqueuedAt.Equal(out[j].EnqueuedAt) {
			return out[i].ID > out[j].ID
		}
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

func (m *Memory) ListNodeRecords(_ context.Context, opts core.ListNodeRecordsOpts) ([]core.JobRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []core.JobRecord
	for _, r := range m.records {
		if r.Kind != core.JobKindNode {
			continue
		}
		if opts.Tenant != "" && r.Tenant != opts.Tenant {
			continue
		}
		if opts.Workspace != "" && r.Workspace != opts.Workspace {
			continue
		}
		if opts.Status != "" && r.Status != opts.Status {
			continue
		}
		if opts.GraphRunID != "" && r.GraphRunID != opts.GraphRunID {
			continue
		}
		if opts.GraphID != "" && r.GraphID != opts.GraphID {
			continue
		}
		if opts.HasOutputPort != "" {
			if r.Result == nil {
				continue
			}
			if _, ok := r.Result.Output[opts.HasOutputPort]; !ok {
				continue
			}
		}
		out = append(out, *r)
	}
	// Match the Postgres store's "enqueued_at DESC, id DESC": id breaks
	// enqueued_at ties so pagination is a stable total order (plain sort.Slice
	// isn't even stable), and a tie on a page boundary can't repeat or drop a
	// row across LIMIT/OFFSET pages. NewestByFinished swaps the leading column
	// for finished_at, nulls last, on the same tiebreaker.
	sort.Slice(out, func(i, j int) bool {
		if opts.NewestByFinished {
			a, b := out[i].FinishedAt, out[j].FinishedAt
			switch {
			case a == nil && b == nil: // both unfinished — fall through to id
			case a == nil:
				return false
			case b == nil:
				return true
			case !a.Equal(*b):
				return a.After(*b)
			}
			return out[i].ID > out[j].ID
		}
		if out[i].EnqueuedAt.Equal(out[j].EnqueuedAt) {
			return out[i].ID > out[j].ID
		}
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
		limit = 100
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
	// Match the Postgres store's "enqueued_at DESC, id DESC": id breaks
	// enqueued_at ties so pagination is a stable total order (plain sort.Slice
	// isn't even stable), and a tie on a page boundary can't repeat or drop a
	// row across LIMIT/OFFSET pages.
	sort.Slice(out, func(i, j int) bool {
		if out[i].EnqueuedAt.Equal(out[j].EnqueuedAt) {
			return out[i].ID > out[j].ID
		}
		return out[i].EnqueuedAt.After(out[j].EnqueuedAt)
	})
	return out, nil
}
