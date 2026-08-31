// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/engine/jobstore"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Gated on DAZYFLOW_TEST_DB (a real Postgres), like the jobstore/auth
// integration tests.
func pgBusPool(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()
	url := os.Getenv("DAZYFLOW_TEST_DB")
	if url == "" {
		t.Skip("set DAZYFLOW_TEST_DB to run Postgres bus tests")
	}
	ctx, cancel := context.WithCancel(context.Background())
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		cancel()
		t.Fatalf("pgxpool.New: %v", err)
	}
	// Single cleanup so ordering is explicit: cancel ctx FIRST (so the
	// listener goroutine drops its held connection), THEN close the
	// pool. The reverse (two separate LIFO cleanups) deadlocks —
	// pool.Close waits on the conn the listener won't release until ctx
	// is done.
	t.Cleanup(func() {
		cancel()
		pool.Close()
	})
	if _, err := pool.Exec(ctx, pgBusSchema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	_, _ = pool.Exec(ctx, "TRUNCATE bus_events")
	return pool, ctx
}

func recv(t *testing.T, ch <-chan BusEvent, within time.Duration) BusEvent {
	t.Helper()
	select {
	case ev := <-ch:
		return ev
	case <-time.After(within):
		t.Fatal("timed out waiting for bus event")
		return BusEvent{}
	}
}

// TestPgBus_CrossInstance is the core HA property: an event published on
// one dzd reaches a subscriber on a *different* dzd.
func TestPgBus_CrossInstance(t *testing.T) {
	pool, ctx := pgBusPool(t)
	busA, err := NewPgBus(ctx, pool) // "node A"
	if err != nil {
		t.Fatalf("busA: %v", err)
	}
	busB, err := NewPgBus(ctx, pool) // "node B"
	if err != nil {
		t.Fatalf("busB: %v", err)
	}

	ch, cancel := busB.Subscribe("run-1")
	defer cancel()
	// Give B's listener a moment to establish LISTEN before A publishes.
	time.Sleep(200 * time.Millisecond)

	busA.Publish("run-1", BusEvent{
		NodeStatus: &NodeStatusEvent{NodeID: "n1", Status: core.JobStatusSucceeded},
	})

	ev := recv(t, ch, 3*time.Second)
	if ev.NodeStatus == nil || ev.NodeStatus.NodeID != "n1" {
		t.Errorf("cross-instance event = %+v, want node n1", ev)
	}
}

// TestPgBus_TerminalRoundTrip checks a full terminal event (with a
// GraphResult) survives the JSON spool round-trip.
func TestPgBus_TerminalRoundTrip(t *testing.T) {
	pool, ctx := pgBusPool(t)
	bus, err := NewPgBus(ctx, pool)
	if err != nil {
		t.Fatalf("bus: %v", err)
	}
	ch, cancel := bus.Subscribe("run-2")
	defer cancel()
	time.Sleep(200 * time.Millisecond)

	bus.Publish("run-2", BusEvent{
		Terminal: &TerminalEvent{
			JobID:    "run-2",
			Status:   core.JobStatusSucceeded,
			GraphRes: engine.GraphResult{GraphID: "g", Status: core.StatusOK},
		},
	})
	ev := recv(t, ch, 3*time.Second)
	if ev.Terminal == nil || ev.Terminal.JobID != "run-2" || ev.Terminal.GraphRes.GraphID != "g" {
		t.Errorf("terminal round-trip = %+v", ev)
	}
}

// TestPgBus_NoCrossTalk: a subscriber for one job doesn't receive
// another job's events.
func TestPgBus_NoCrossTalk(t *testing.T) {
	pool, ctx := pgBusPool(t)
	bus, err := NewPgBus(ctx, pool)
	if err != nil {
		t.Fatalf("bus: %v", err)
	}
	ch, cancel := bus.Subscribe("run-3")
	defer cancel()
	time.Sleep(200 * time.Millisecond)

	bus.Publish("run-OTHER", BusEvent{
		NodeStatus: &NodeStatusEvent{NodeID: "x", Status: core.JobStatusFailed},
	})
	select {
	case ev := <-ch:
		t.Errorf("received cross-job event: %+v", ev)
	case <-time.After(500 * time.Millisecond):
		// good — nothing for run-3
	}
}

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
