// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"errors"

	"github.com/dazyflow/dazyflow/workspace"
)

// Keeping the ScheduleStore projection in step with the workspaces.
//
// The scheduler used to derive its enrollment set by loading every flow of
// every tenant from git on a 30s ticker, which is O(all flows in the install)
// per pass and holds each workspace's mutex against the editor while it runs.
// Instead each write that can change what fires re-derives that ONE flow's
// rows here, and a rescan reads the table.
//
// Every such path calls reprojectSchedule; ReconcileSchedules is the periodic
// backstop for a path that doesn't (or a write that failed).

// reprojectSchedule re-derives one flow's schedule rows. Best-effort and
// non-blocking on the caller's behalf: the write it follows has already
// committed, so a projection failure must not turn into the user's error. The
// reconcile pass repairs whatever this drops.
func (s *Service) reprojectSchedule(ctx context.Context, tenant, ws, graphID string) {
	if s.Schedules == nil || tenant == "" || ws == "" || graphID == "" {
		return
	}
	specs, err := s.deriveFlowSchedules(tenant, ws, graphID)
	if err != nil {
		s.logf("schedule projection: derive %s/%s/%s: %v", tenant, ws, graphID, err)
		return
	}
	if err := s.Schedules.ReplaceFlowSchedules(ctx, tenant, ws, graphID, specs); err != nil {
		s.logf("schedule projection: write %s/%s/%s: %v", tenant, ws, graphID, err)
	}
}

// deriveFlowSchedules returns the enrollments a flow currently asks for. A
// flow that is unpublished, disabled, or gone yields none, which is how each
// of those takes a schedule offline.
//
// The cadence is read from the DRAFT while enrollment gates on the published
// tag, matching what fireGraph does: a timing or pause edit takes effect on
// save, but the revision that runs is the published one.
func (s *Service) deriveFlowSchedules(tenant, ws, graphID string) ([]ScheduleSpec, error) {
	store, err := s.Workspaces.Open(tenant, ws)
	if err != nil {
		return nil, err
	}
	pub, err := store.PublishedCommit(graphID)
	if err != nil {
		return nil, err
	}
	if pub == "" {
		return nil, nil
	}
	g, err := store.Load(graphID)
	if err != nil {
		if errors.Is(err, workspace.ErrGraphNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return DeriveScheduleSpecs(scheduleCronParser, tenant, ws, g, s.logf), nil
}

// ReconcileSchedules rebuilds the whole projection from the workspaces: the
// expensive git walk the scheduler no longer does on every rescan, run rarely
// as a backstop against drift.
//
// Safe on every replica, but pointless on more than one — cmd/dzd runs it on
// the scheduler leader. Returns the number of flows examined.
func (s *Service) ReconcileSchedules(ctx context.Context) (int, error) {
	if s.Schedules == nil {
		return 0, nil
	}
	enum, ok := s.Workspaces.(WorkspaceEnumerator)
	if !ok {
		return 0, errors.New("workspace lookup does not support enumeration")
	}
	// Read the projection once so an unchanged flow costs no write. In steady
	// state every flow is unchanged, which is what keeps an hourly pass over a
	// large install from being thousands of pointless transactions.
	stored := make(map[string][]ScheduleSpec)
	if existing, err := s.Schedules.ListSchedules(ctx); err != nil {
		s.logf("schedule reconcile: read projection: %v", err)
	} else {
		for _, spec := range existing {
			k := flowKey(spec.Tenant, spec.Workspace, spec.GraphID)
			stored[k] = append(stored[k], spec)
		}
	}
	live := make(map[string]struct{})
	flows := 0
	for key, store := range enum.All() {
		if err := ctx.Err(); err != nil {
			return flows, err
		}
		tenant, ws, ok := splitKey(key)
		if !ok {
			continue
		}
		ids, err := store.ListGraphs()
		if err != nil {
			s.logf("schedule reconcile: list %s/%s: %v", tenant, ws, err)
			continue
		}
		for _, id := range ids {
			flows++
			key := flowKey(tenant, ws, id)
			live[key] = struct{}{}
			specs, err := s.deriveFlowSchedules(tenant, ws, id)
			if err != nil {
				s.logf("schedule reconcile: derive %s/%s/%s: %v", tenant, ws, id, err)
				continue
			}
			if sameSpecSet(stored[key], specs) {
				continue
			}
			if err := s.Schedules.ReplaceFlowSchedules(ctx, tenant, ws, id, specs); err != nil {
				s.logf("schedule reconcile: write %s/%s/%s: %v", tenant, ws, id, err)
			}
		}
	}
	// Rows whose flow no longer exists: a delete that landed while this dzd
	// was down leaves nothing else to clear them.
	if n, err := s.Schedules.PruneMissingFlows(ctx, live); err != nil {
		s.logf("schedule reconcile: prune: %v", err)
	} else if n > 0 {
		s.logf("schedule reconcile: pruned %d row(s) for deleted flows", n)
	}
	return flows, nil
}

// sameSpecSet reports whether two spec sets are equal ignoring order.
// ScheduleSpec is all comparable fields, so equality is the struct's.
func sameSpecSet(a, b []ScheduleSpec) bool {
	if len(a) != len(b) {
		return false
	}
	if len(a) == 0 {
		return true
	}
	byKey := make(map[string]ScheduleSpec, len(a))
	for _, spec := range a {
		byKey[spec.EntryKey] = spec
	}
	for _, spec := range b {
		if prev, ok := byKey[spec.EntryKey]; !ok || prev != spec {
			return false
		}
	}
	return true
}
