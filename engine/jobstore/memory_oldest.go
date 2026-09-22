// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package jobstore

import (
	"context"
	"math"
	"slices"

	"github.com/dazyflow/dazyflow/core"
)

// The Memory side of postgres_oldest.go.

func (m *Memory) ListGraphRunsOldest(ctx context.Context, opts core.ListGraphRunsOpts) ([]core.JobRecord, error) {
	page := opts
	page.Limit, page.Offset = math.MaxInt32, 0
	all, err := m.ListGraphRuns(ctx, page)
	if err != nil {
		return nil, err
	}
	slices.Reverse(all) // newest-first with id DESC ties → oldest-first with id ASC
	if opts.Offset > 0 {
		if opts.Offset >= len(all) {
			return nil, nil
		}
		all = all[opts.Offset:]
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit < len(all) {
		all = all[:limit]
	}
	return all, nil
}

func (m *Memory) ListGraphRunSummariesOldest(ctx context.Context, opts core.ListGraphRunsOpts) ([]core.RunSummary, error) {
	recs, err := m.ListGraphRunsOldest(ctx, opts)
	if err != nil {
		return nil, err
	}
	out := make([]core.RunSummary, 0, len(recs))
	for _, rec := range recs {
		out = append(out, core.SummarizeRun(rec))
	}
	return out, nil
}

func (m *Memory) GraphRunTenants(_ context.Context, status core.JobStatus) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	seen := map[string]struct{}{}
	var out []string
	for _, r := range m.records {
		if r.Kind != core.JobKindGraph || r.Status != status {
			continue
		}
		if _, dup := seen[r.Tenant]; dup {
			continue
		}
		seen[r.Tenant] = struct{}{}
		out = append(out, r.Tenant)
	}
	slices.Sort(out)
	return out, nil
}

func (m *Memory) MarkGraphRunningCapped(_ context.Context, jobID, tenant string, limit int) (started, atCap bool, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.records[jobID]
	if !ok {
		return false, false, core.ErrNotFound
	}
	running := 0
	for _, o := range m.records {
		if o.Kind == core.JobKindGraph && o.Tenant == tenant && o.Status == core.JobStatusRunning {
			running++
		}
	}
	if running >= limit {
		return false, true, nil
	}
	if r.Kind != core.JobKindGraph || r.Status != core.JobStatusQueued {
		return false, false, nil
	}
	r.Status = core.JobStatusRunning
	if r.StartedAt == nil {
		now := m.clock()
		r.StartedAt = &now
	}
	return true, false, nil
}
