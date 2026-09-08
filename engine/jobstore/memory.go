// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package jobstore

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/dazyflow/dazyflow/core"
)

type Memory struct {
	mu             sync.Mutex
	records        map[string]*core.JobRecord
	clock          func() time.Time
	maxConcurrent  int // per-tenant running-node cap; 0 = unlimited
	slots          map[string]time.Time
	burstSpacing   time.Duration
	notified       map[string]bool
	notifyAttempts map[string]int
}

func NewMemory() *Memory {
	return &Memory{
		records:        make(map[string]*core.JobRecord),
		slots:          make(map[string]time.Time),
		clock:          time.Now,
		burstSpacing:   DefaultBurstSpacing,
		notified:       make(map[string]bool),
		notifyAttempts: make(map[string]int),
	}
}

// SetBurstSpacing must be called once at startup.
func (m *Memory) SetBurstSpacing(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.burstSpacing = d
}

func (m *Memory) DeleteByTenant(_ context.Context, tenant string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for id, r := range m.records {
		if r.Tenant == tenant {
			delete(m.slots, id)
			delete(m.records, id)
			n++
		}
	}
	return n, nil
}

// SetMaxConcurrentPerTenant withholds new queued work from a tenant at the cap.
// Reclaiming an expired lease is exempt, being recovery of existing work rather
// than new concurrency. 0 means no cap; set once at startup.
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

// EnqueueNodes keeps the Postgres store's all-or-nothing contract, so the
// conformance suite can hold both to it. Every record is checked before any is
// written, because a caller falling back after a partial write would enqueue some
// of them twice.
func (m *Memory) EnqueueNodes(_ context.Context, recs []core.JobRecord) (int, error) {
	if len(recs) == 0 {
		return 0, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	seen := make(map[string]struct{}, len(recs))
	for _, r := range recs {
		if _, exists := m.records[r.ID]; exists {
			return 0, core.ErrConflict
		}
		// A duplicate WITHIN the batch would pass the check above and then overwrite
		// its twin, making the returned count a lie.
		if _, dup := seen[r.ID]; dup {
			return 0, core.ErrConflict
		}
		seen[r.ID] = struct{}{}
	}
	for _, r := range recs {
		if err := m.enqueueLocked(r); err != nil {
			return 0, err
		}
	}
	return len(recs), nil
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
	if rec.Status == "" {
		rec.Status = core.JobStatusQueued
	}
	// A seed or a graph-record never passes through Claim, so stamp its start — and
	// a terminal seed's finish — here, or run durations don't render.
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
	slot := rec.EnqueuedAt
	if rec.Kind == core.JobKindNode && rec.Status == core.JobStatusQueued && m.burstSpacing > 0 {
		var tail time.Time
		for id, r := range m.records {
			if r.Kind == core.JobKindNode && r.Status == core.JobStatusQueued && r.Tenant == rec.Tenant {
				if t := m.slots[id]; t.After(tail) {
					tail = t
				}
			}
		}
		if !tail.IsZero() && tail.Add(m.burstSpacing).After(slot) {
			slot = tail.Add(m.burstSpacing)
		}
	}
	m.slots[rec.ID] = slot
	copy := rec
	m.records[rec.ID] = &copy
	return nil
}

func (m *Memory) Claim(_ context.Context, worker string, lease time.Duration) (core.JobRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.clock()

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
		if r.Kind != core.JobKindNode {
			continue
		}
		if r.AvailableAt != nil && r.AvailableAt.After(now) {
			continue
		}
		if r.Status == core.JobStatusQueued {
			if m.maxConcurrent > 0 && runningByTenant[r.Tenant] >= m.maxConcurrent {
				continue
			}
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
	slot := func(r *core.JobRecord) time.Time {
		if t, ok := m.slots[r.ID]; ok {
			return t
		}
		return r.EnqueuedAt
	}
	sort.Slice(candidates, func(i, j int) bool {
		if si, sj := slot(candidates[i]), slot(candidates[j]); !si.Equal(sj) {
			return si.Before(sj)
		}
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
		// Terminal records can't be revived: a caller wanting to try again must pick a
		// new record ID.
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

func (m *Memory) CompleteOwned(_ context.Context, jobID, worker string, status core.JobStatus, result *core.Result) error {
	return m.complete(jobID, worker, status, result)
}

// ClaimUnnotified holds the lock as its transaction, so the claim is atomic the
// same way the Postgres CTE is.
func (m *Memory) ClaimUnnotified(_ context.Context, lookback time.Duration, maxAttempts, limit int) ([]core.JobRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	since := m.clock().Add(-lookback)
	m.mu.Lock()
	defer m.mu.Unlock()
	var due []*core.JobRecord
	for _, rec := range m.records {
		if rec.Kind != core.JobKindGraph || rec.FinishedAt == nil {
			continue
		}
		if rec.Status != core.JobStatusFailed && rec.Status != core.JobStatusCancelled {
			continue
		}
		if m.notified[rec.ID] || m.notifyAttempts[rec.ID] >= maxAttempts {
			continue
		}
		if rec.FinishedAt.Before(since) {
			continue
		}
		due = append(due, rec)
	}
	// Oldest finish first, so a backlog is worked in the order it happened.
	sort.Slice(due, func(a, b int) bool { return due[a].FinishedAt.Before(*due[b].FinishedAt) })
	if len(due) > limit {
		due = due[:limit]
	}
	out := make([]core.JobRecord, 0, len(due))
	for _, rec := range due {
		m.notified[rec.ID] = true
		m.notifyAttempts[rec.ID]++
		out = append(out, *rec)
	}
	return out, nil
}

func (m *Memory) ReleaseNotifyClaim(_ context.Context, jobID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.notified, jobID)
	return nil
}

// CompleteAndEnqueue holds one lock as its transaction.
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
	// A worker that lost its lease must not be able to write a result.
	if worker != "" && r.WorkerID != worker {
		return core.ErrConflict
	}
	if !core.IsTerminalStatus(status) && status != core.JobStatusAwaiting {
		return core.ErrConflict
	}
	if core.IsTerminalStatus(r.Status) {
		return core.ErrConflict
	}
	// awaiting → awaiting means a node that already parked executed a second time,
	// and letting it through announced one pause twice — which the park hook turned
	// into a duplicate approval email. The two stores must agree on ErrConflict.
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

// SetGraphRunParked counts only the transition out of the expected status, so
// repeat parks and non-final resumes are no-ops and a terminal record is never
// revived.
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

func (m *Memory) Outcomes(_ context.Context, jobIDs []string) (map[string]core.NodeOutcome, error) {
	if len(jobIDs) == 0 {
		return nil, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]core.NodeOutcome, len(jobIDs))
	for _, id := range jobIDs {
		r, ok := m.records[id]
		if !ok {
			continue
		}
		oc := core.NodeOutcome{Status: r.Status}
		if r.Result != nil {
			res := *r.Result
			oc.Result = &res
		}
		out[id] = oc
	}
	return out, nil
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
		// Since is inclusive, Until exclusive, mirroring the Postgres predicates.
		if !opts.Since.IsZero() && r.EnqueuedAt.Before(opts.Since) {
			continue
		}
		if !opts.Until.IsZero() && !r.EnqueuedAt.Before(opts.Until) {
			continue
		}
		out = append(out, *r)
	}
	// The Postgres store's "enqueued_at DESC, id DESC": id breaks ties so
	// pagination is a stable total order, and a tie on a page boundary cannot
	// repeat or drop a row across pages.
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

func (m *Memory) ListGraphRunSummaries(ctx context.Context, opts core.ListGraphRunsOpts) ([]core.RunSummary, error) {
	recs, err := m.ListGraphRuns(ctx, opts)
	if err != nil {
		return nil, err
	}
	var out []core.RunSummary
	for _, rec := range recs {
		out = append(out, core.SummarizeRun(rec))
	}
	return out, nil
}

func (m *Memory) GetGraphRunSummary(ctx context.Context, jobID string) (core.RunSummary, error) {
	rec, err := m.Get(ctx, jobID)
	if err != nil {
		return core.RunSummary{}, err
	}
	return core.SummarizeRun(rec), nil
}

func (m *Memory) CountGraphRuns(ctx context.Context, opts core.ListGraphRunsOpts) (int, error) {
	// Limit means "count no further than", so an unset one must not fall through to
	// ListGraphRuns' default page of 50 and under-report.
	if opts.Limit <= 0 {
		opts.Limit = len(m.records) + 1
	}
	recs, err := m.ListGraphRuns(ctx, opts)
	if err != nil {
		return 0, err
	}
	return len(recs), nil
}

func (m *Memory) ListNodeRuns(ctx context.Context, graphRunID string, limit int) ([]core.NodeRun, error) {
	recs, err := m.ListNodeRecords(ctx, core.ListNodeRecordsOpts{GraphRunID: graphRunID, Limit: limit})
	if err != nil {
		return nil, err
	}
	out := make([]core.NodeRun, 0, len(recs))
	for _, rec := range recs {
		out = append(out, core.SummarizeNodeRun(rec))
	}
	return out, nil
}

// matchesNodeRecord is shared so the list and the count cannot drift — the same
// reason the Postgres store builds one WHERE clause for both.
func matchesNodeRecord(r *core.JobRecord, opts core.ListNodeRecordsOpts) bool {
	if r.Kind != core.JobKindNode {
		return false
	}
	if opts.Tenant != "" && r.Tenant != opts.Tenant {
		return false
	}
	if opts.Workspace != "" && r.Workspace != opts.Workspace {
		return false
	}
	if opts.Status != "" && r.Status != opts.Status {
		return false
	}
	if opts.GraphRunID != "" && r.GraphRunID != opts.GraphRunID {
		return false
	}
	if opts.GraphID != "" && r.GraphID != opts.GraphID {
		return false
	}
	if opts.HasOutputPort != "" {
		if r.Result == nil {
			return false
		}
		if _, ok := r.Result.Output[opts.HasOutputPort]; !ok {
			return false
		}
	}
	return true
}

// CountNodeRecords needs no sort or record copy: order decides WHICH rows a
// limit keeps, but a count only needs how many, and the ceiling clips the same
// total either way.
func (m *Memory) CountNodeRecords(_ context.Context, opts core.ListNodeRecordsOpts) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, r := range m.records {
		if matchesNodeRecord(r, opts) {
			n++
		}
	}
	n = max(n-opts.Offset, 0)
	if opts.Limit > 0 && n > opts.Limit {
		n = opts.Limit
	}
	return n, nil
}

func (m *Memory) ListNodeRecords(_ context.Context, opts core.ListNodeRecordsOpts) ([]core.JobRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []core.JobRecord
	for _, r := range m.records {
		if !matchesNodeRecord(r, opts) {
			continue
		}
		out = append(out, *r)
	}
	// The Postgres store's "enqueued_at DESC, id DESC": id breaks ties so
	// pagination is a stable total order, and a tie on a page boundary cannot
	// repeat or drop a row across pages. NewestByFinished swaps the leading column
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
	// The Postgres store's "enqueued_at DESC, id DESC": id breaks ties so
	// pagination is a stable total order, and a tie on a page boundary cannot
	// repeat or drop a row across pages.
	sort.Slice(out, func(i, j int) bool {
		if out[i].EnqueuedAt.Equal(out[j].EnqueuedAt) {
			return out[i].ID > out[j].ID
		}
		return out[i].EnqueuedAt.After(out[j].EnqueuedAt)
	})
	return out, nil
}
