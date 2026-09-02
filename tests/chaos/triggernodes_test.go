// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package chaos

import (
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
)

// core.MaxGraphTriggers caps len(Graph.Triggers), and the scheduler keys a
// graph-level entry by the schedule itself so identical ones collapse
// (TestTriggerArray_IsCapped). Neither holds for a trigger NODE: the schedule
// moved onto cron_trigger / poll_trigger steps, the scheduler keys those by
// NODE ID (deliberately — each node carries its own cursor), and nothing counts
// them. So the shape that test closed is reachable again by pasting the step
// instead of the trigger: N identical Schedule steps are N scheduler entries
// and N runs of the whole flow per tick, bounded only by MaxGraphNodes (1000).
func TestTriggerNodeArray_IsCapped(t *testing.T) {
	h := newHarness(t)

	flood := graph("crontrignodes", nil, nil)
	for i := range 200 {
		flood.Nodes = append(flood.Nodes, core.Node{
			ID:     "t" + itoa(i),
			Module: "cron_trigger",
			Params: map[string]any{"cron": "* * * * *"},
		})
	}
	if err := h.publish(t, flood); err == nil {
		t.Errorf("FINDING: %d Schedule steps on one flow were stored", len(flood.Nodes))
	} else {
		t.Logf("refused at the save gate: %v", firstLine(err))
	}

	// At the cap the flow saves, and the scheduler's node-keyed entries are
	// then bounded by it: MaxGraphTriggers fires a minute, not MaxGraphNodes.
	g := graph("crontrigcap", nil, nil)
	for i := range core.MaxGraphTriggers {
		g.Nodes = append(g.Nodes, core.Node{
			ID:     "t" + itoa(i),
			Module: "cron_trigger",
			Params: map[string]any{"cron": "* * * * *"},
		})
	}
	if err := h.publish(t, g); err != nil {
		t.Fatalf("trigger steps at the cap were refused: %v", err)
	}

	cs := newClockedScheduler(t, h)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && cs.sched.TrackedCount() == 0 {
		time.Sleep(20 * time.Millisecond)
	}
	if tracked := cs.sched.TrackedCount(); tracked > core.MaxGraphTriggers {
		t.Errorf("FINDING: %d Schedule steps produced %d scheduler entries",
			core.MaxGraphTriggers, tracked)
	} else {
		t.Logf("%d Schedule steps at the cap → %d scheduler entries", core.MaxGraphTriggers, tracked)
	}
}

// The same lever on the Poll step, which is the sharper one: a poll interval is
// a number the author types, the scheduler's only bounds are "> 0" and "<= one
// year", and the interval is anchored to the last fire rather than a wall-clock
// boundary. 200 steps at interval_seconds=1 was 200 runs a second from one
// saved flow; at the node ceiling it was 1000.
func TestPollTriggerNodes_AreCapped(t *testing.T) {
	h := newHarness(t)
	g := graph("polltrignodes", nil, nil)
	for i := range 200 {
		g.Nodes = append(g.Nodes, core.Node{
			ID:     "p" + itoa(i),
			Module: "poll_trigger",
			Params: map[string]any{"interval_seconds": 1},
		})
	}
	if err := h.publish(t, g); err == nil {
		t.Errorf("FINDING: %d Poll steps at interval_seconds=1 on one flow were stored", len(g.Nodes))
	} else {
		t.Logf("refused at the save gate: %v", firstLine(err))
	}
}

// The cap counts the array and the steps TOGETHER: both become scheduler
// entries, so splitting the flood across the two used to buy twice the ceiling.
func TestTriggerArrayAndSteps_ShareOneCap(t *testing.T) {
	h := newHarness(t)
	g := graph("trigmixed", nil, nil)
	for i := range core.MaxGraphTriggers {
		g.Nodes = append(g.Nodes, core.Node{
			ID:     "t" + itoa(i),
			Module: "cron_trigger",
			Params: map[string]any{"cron": "* * * * *"},
		})
	}
	for range core.MaxGraphTriggers {
		g.Triggers = append(g.Triggers, core.GraphTrigger{Type: "cron", Cron: "* * * * *"})
	}
	if err := h.publish(t, g); err == nil {
		t.Errorf("FINDING: %d trigger steps plus %d declared triggers (%d entries) were stored under a cap of %d",
			core.MaxGraphTriggers, core.MaxGraphTriggers, 2*core.MaxGraphTriggers, core.MaxGraphTriggers)
	} else {
		t.Logf("refused at the save gate: %v", firstLine(err))
	}
}

// itoa avoids pulling strconv into every case file.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [8]byte
	n := len(b)
	for i > 0 {
		n--
		b[n] = byte('0' + i%10)
		i /= 10
	}
	return string(b[n:])
}
