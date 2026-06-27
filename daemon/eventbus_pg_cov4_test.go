// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/engine/jobstore"
)

// TestPgBus_DeleteByTenant covers the erasure-cascade hook: spooled events for
// a tenant's runs are removed via the jobs join, leaving other tenants' events.
func TestPgBus_DeleteByTenant(t *testing.T) {
	pool, ctx := pgBusPool(t)

	js, err := jobstore.NewPostgresFromPool(ctx, pool)
	if err != nil {
		t.Fatalf("jobstore schema: %v", err)
	}
	if _, err := pool.Exec(ctx, "TRUNCATE jobs"); err != nil {
		t.Fatalf("truncate jobs: %v", err)
	}

	bus, err := NewPgBus(ctx, pool)
	if err != nil {
		t.Fatalf("NewPgBus: %v", err)
	}

	// Two jobs owned by different tenants.
	for id, tenant := range map[string]string{"run-acme": "acme", "run-other": "elsewhere"} {
		if err := js.Enqueue(ctx, core.JobRecord{
			ID: id, Kind: core.JobKindGraph, Tenant: tenant, Workspace: "ws",
			GraphID: "g", NodeID: "*", Status: core.JobStatusRunning,
			Job: core.Job{ID: id, GraphID: "g"},
		}); err != nil {
			t.Fatalf("enqueue %s: %v", id, err)
		}
	}

	// Spool an event for each run.
	bus.Publish("run-acme", BusEvent{NodeStatus: &NodeStatusEvent{NodeID: "n", Status: core.JobStatusRunning}})
	bus.Publish("run-other", BusEvent{NodeStatus: &NodeStatusEvent{NodeID: "n", Status: core.JobStatusRunning}})

	n, err := bus.DeleteByTenant(ctx, "acme")
	if err != nil {
		t.Fatalf("DeleteByTenant: %v", err)
	}
	if n != 1 {
		t.Fatalf("DeleteByTenant = %d, want 1", n)
	}

	// elsewhere's event survives.
	var remaining int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM bus_events WHERE job_id = 'run-other'`).Scan(&remaining); err != nil {
		t.Fatalf("count: %v", err)
	}
	if remaining != 1 {
		t.Fatalf("other tenant events remaining = %d, want 1", remaining)
	}
}
