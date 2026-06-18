package daemon

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/engine"
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
