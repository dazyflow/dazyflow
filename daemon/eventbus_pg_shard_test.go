// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Reading only what this replica is watching.
//
// Every replica used to read every event in the fleet on every wake and hand
// almost all of them to nobody — most runs are watched by no one, and a watched
// one by a single replica. Now a notification names the run, so a replica with
// no subscriber for it moves its cursor on without touching the spool.
//
// Moving that cursor without reading is the part that needs guarding: it must
// not skip an event somebody here is owed, and it must still leave a later
// subscriber starting from "now" rather than replaying an hour of spool.

package daemon

import (
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
)

func TestParseBusNotice(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in    string
		id    int64
		jobID string
		ok    bool
	}{
		{"42:run-1", 42, "run-1", true},
		{"1:a:b", 1, "a:b", true}, // a job id containing a colon still parses
		{"", 0, "", false},
		{"run-1", 0, "", false},  // an older publisher's bare job id
		{":run-1", 0, "", false}, // no id
		{"notanumber:run-1", 0, "", false},
	}
	for _, c := range cases {
		id, jobID, ok := parseBusNotice(c.in)
		if ok != c.ok || id != c.id || jobID != c.jobID {
			t.Errorf("parseBusNotice(%q) = (%d, %q, %v), want (%d, %q, %v)",
				c.in, id, jobID, ok, c.id, c.jobID, c.ok)
		}
	}
}

// A run this replica is NOT watching must not stall or divert the run it IS.
func TestPgBus_UnwatchedTrafficDoesNotDisturbAWatchedRun(t *testing.T) {
	pool, ctx := pgBusPool(t)
	publisher, err := NewPgBus(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	watcher, err := NewPgBus(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	ch, cancel := watcher.Subscribe("watched")
	defer cancel()
	time.Sleep(200 * time.Millisecond)

	// A burst of traffic for runs nobody here is watching, interleaved with the
	// one that is.
	for i := range 20 {
		publisher.Publish("noise-"+string(rune('a'+i%5)), BusEvent{
			NodeStatus: &NodeStatusEvent{NodeID: "x", Status: core.JobStatusRunning},
		})
	}
	publisher.Publish("watched", BusEvent{
		NodeStatus: &NodeStatusEvent{NodeID: "mine", Status: core.JobStatusSucceeded},
	})

	ev := recv(t, ch, 5*time.Second)
	if ev.NodeStatus == nil || ev.NodeStatus.NodeID != "mine" {
		t.Fatalf("watched run received %+v, want its own event", ev)
	}
	// And nothing from the noise leaked in.
	select {
	case extra := <-ch:
		t.Fatalf("received an event for a run this replica does not watch: %+v", extra)
	case <-time.After(300 * time.Millisecond):
	}
}

// The cursor moves on while nothing is watched, so a subscriber that appears
// later starts from now — it must not be handed the spool's backlog.
func TestPgBus_SubscribingAfterUnwatchedTrafficDoesNotReplay(t *testing.T) {
	pool, ctx := pgBusPool(t)
	publisher, err := NewPgBus(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	idle, err := NewPgBus(ctx, pool) // watching nothing at all
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)

	// History for a run, published while nobody is subscribed anywhere.
	for i := range 10 {
		publisher.Publish("run-later", BusEvent{
			NodeStatus: &NodeStatusEvent{
				NodeID: "before-subscribe", Status: core.JobStatusRunning,
			},
		})
		_ = i
	}
	time.Sleep(400 * time.Millisecond) // let the idle replica move its cursor

	// Now somebody watches it. They are owed what comes next, not the backlog.
	ch, cancel := idle.Subscribe("run-later")
	defer cancel()
	time.Sleep(200 * time.Millisecond)

	publisher.Publish("run-later", BusEvent{
		NodeStatus: &NodeStatusEvent{NodeID: "after-subscribe", Status: core.JobStatusSucceeded},
	})

	ev := recv(t, ch, 5*time.Second)
	if ev.NodeStatus == nil || ev.NodeStatus.NodeID != "after-subscribe" {
		t.Fatalf("first event after subscribing = %+v — the backlog was replayed", ev)
	}
}
