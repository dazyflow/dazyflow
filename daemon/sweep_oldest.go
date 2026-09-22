// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"slices"

	"github.com/dazyflow/dazyflow/core"
)

// The background sweeps (promotion, reaping) must work their backlog oldest
// first. ListGraphRuns is newest-first for the Runs page, so a sweep reading one
// capped page of it never reached the oldest pending or stuck runs once the
// backlog outgrew the page. Both stores implement these; the fallbacks keep a
// store without them correct, just slower.

type oldestGraphRunLister interface {
	ListGraphRunsOldest(ctx context.Context, opts core.ListGraphRunsOpts) ([]core.JobRecord, error)
}

type oldestRunSummaryLister interface {
	ListGraphRunSummariesOldest(ctx context.Context, opts core.ListGraphRunsOpts) ([]core.RunSummary, error)
}

type graphRunTenantLister interface {
	GraphRunTenants(ctx context.Context, status core.JobStatus) ([]string, error)
}

// cappedGraphRunStarter makes the concurrency check and the start one atomic
// step, so promoters on several replicas cannot overshoot a tenant's cap.
type cappedGraphRunStarter interface {
	MarkGraphRunningCapped(ctx context.Context, jobID, tenant string, limit int) (started, atCap bool, err error)
}

// fallbackListCeiling bounds how far the fallbacks page through a newest-first
// listing to find its oldest end.
const fallbackListCeiling = 10000

func listGraphRunsOldest(ctx context.Context, store core.JobStore, opts core.ListGraphRunsOpts) ([]core.JobRecord, error) {
	if l, ok := store.(oldestGraphRunLister); ok {
		return l.ListGraphRunsOldest(ctx, opts)
	}
	all, err := pageAll(opts, func(o core.ListGraphRunsOpts) ([]core.JobRecord, error) {
		return store.ListGraphRuns(ctx, o)
	})
	if err != nil {
		return nil, err
	}
	return oldestWindow(all, opts), nil
}

func listRunSummariesOldest(ctx context.Context, store core.JobStore, opts core.ListGraphRunsOpts) ([]core.RunSummary, error) {
	if l, ok := store.(oldestRunSummaryLister); ok {
		return l.ListGraphRunSummariesOldest(ctx, opts)
	}
	all, err := pageAll(opts, func(o core.ListGraphRunsOpts) ([]core.RunSummary, error) {
		return core.ListRunSummaries(ctx, store, o)
	})
	if err != nil {
		return nil, err
	}
	return oldestWindow(all, opts), nil
}

func graphRunTenants(ctx context.Context, store core.JobStore, status core.JobStatus) ([]string, error) {
	if l, ok := store.(graphRunTenantLister); ok {
		return l.GraphRunTenants(ctx, status)
	}
	all, err := pageAll(core.ListGraphRunsOpts{Status: status}, func(o core.ListGraphRunsOpts) ([]core.RunSummary, error) {
		return core.ListRunSummaries(ctx, store, o)
	})
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	var out []string
	for _, r := range all {
		if _, dup := seen[r.Tenant]; !dup {
			seen[r.Tenant] = struct{}{}
			out = append(out, r.Tenant)
		}
	}
	return out, nil
}

// pageAll reads a newest-first listing to its end (or the ceiling).
func pageAll[T any](opts core.ListGraphRunsOpts, list func(core.ListGraphRunsOpts) ([]T, error)) ([]T, error) {
	const page = 200
	opts.Limit, opts.Offset = page, 0
	var all []T
	for len(all) < fallbackListCeiling {
		batch, err := list(opts)
		if err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if len(batch) < page {
			break
		}
		opts.Offset += len(batch)
	}
	return all, nil
}

// oldestWindow turns a full newest-first listing into opts' oldest-first page.
func oldestWindow[T any](newestFirst []T, opts core.ListGraphRunsOpts) []T {
	slices.Reverse(newestFirst)
	out := newestFirst
	if opts.Offset > 0 {
		if opts.Offset >= len(out) {
			return nil
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
	return out
}
