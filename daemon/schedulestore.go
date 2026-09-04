// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"sort"
	"sync"
)

// ScheduleStore holds the scheduler's enrollment set as data, so a rescan is
// one query instead of a git load per flow across every tenant.
//
// It is a PROJECTION, not a source of truth: the flow's own graph and its
// published tag remain authoritative, and every write path that can change
// either re-derives this flow's rows. A projection that drifts costs a
// schedule that silently stops firing, which is why Service keeps a periodic
// reconcile against the workspaces (see ReconcileSchedules).
type ScheduleStore interface {
	// ListSchedules returns every enrolled schedule across all tenants.
	ListSchedules(ctx context.Context) ([]ScheduleSpec, error)

	// ReplaceFlowSchedules atomically makes specs the complete set for one
	// flow. An empty slice removes the flow from the schedule set — that is
	// how unpublishing, disabling, and deleting a flow all take effect.
	ReplaceFlowSchedules(ctx context.Context, tenant, workspace, graphID string, specs []ScheduleSpec) error

	// DeleteByTenant removes every schedule owned by a tenant, for the GDPR
	// erasure cascade.
	DeleteByTenant(ctx context.Context, tenant string) (int, error)

	// PruneMissingFlows removes rows for flows absent from live, keyed
	// tenant/workspace/graphID. It is the delete half of a reconcile: a flow
	// deleted while this dzd was down leaves rows nothing else will clear.
	PruneMissingFlows(ctx context.Context, live map[string]struct{}) (int, error)
}

// MemScheduleStore is the in-memory ScheduleStore used by tests and by
// single-node deployments running without Postgres.
type MemScheduleStore struct {
	mu sync.Mutex
	// byFlow keys the complete spec set of one flow, so a replace is a single
	// map write and can't leave a half-updated flow behind.
	byFlow map[string][]ScheduleSpec
}

func NewMemScheduleStore() *MemScheduleStore {
	return &MemScheduleStore{byFlow: map[string][]ScheduleSpec{}}
}

func flowKey(tenant, workspace, graphID string) string {
	return tenant + "/" + workspace + "/" + graphID
}

func (m *MemScheduleStore) ListSchedules(context.Context) ([]ScheduleSpec, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []ScheduleSpec
	for _, specs := range m.byFlow {
		out = append(out, specs...)
	}
	// Deterministic order so tests don't depend on map iteration.
	sort.Slice(out, func(i, j int) bool { return out[i].EntryKey < out[j].EntryKey })
	return out, nil
}

func (m *MemScheduleStore) ReplaceFlowSchedules(_ context.Context, tenant, workspace, graphID string, specs []ScheduleSpec) error {
	k := flowKey(tenant, workspace, graphID)
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(specs) == 0 {
		delete(m.byFlow, k)
		return nil
	}
	m.byFlow[k] = append([]ScheduleSpec(nil), specs...)
	return nil
}

func (m *MemScheduleStore) DeleteByTenant(_ context.Context, tenant string) (int, error) {
	prefix := tenant + "/"
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for k, specs := range m.byFlow {
		if len(k) > len(prefix) && k[:len(prefix)] == prefix {
			n += len(specs)
			delete(m.byFlow, k)
		}
	}
	return n, nil
}

func (m *MemScheduleStore) PruneMissingFlows(_ context.Context, live map[string]struct{}) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for k, specs := range m.byFlow {
		if _, ok := live[k]; !ok {
			n += len(specs)
			delete(m.byFlow, k)
		}
	}
	return n, nil
}
