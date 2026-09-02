// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package chaos

import (
	"strings"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
)

// TestIdentifierBytes_AreCapped closed the byte ceiling against the strings
// that NAME things (node IDs, module names, edge ports). A graph also carries
// two repeated SUB-RECORDS — frames and triggers — and the walk charged some
// of each one's strings but not all: a frame's Title and Color but not its ID,
// a trigger's Cron, TZ, Secret and FormTitle but not its Type.
//
// Both of the missed fields are free-form and unvalidated. Nothing anywhere
// looks at a frame ID (frames are editor-only comment boxes the engine
// ignores), and the scheduler switches on a trigger's Type and ignores what it
// doesn't recognize. The count ceilings beside them — MaxGraphFrames (1000)
// and MaxGraphTriggers (32) — bound how MANY sub-records a graph carries, not
// how big one is, which is the same confusion TestIdentifierBytes_AreCapped
// was written about.
func TestFrameIDBytes_AreCapped(t *testing.T) {
	const (
		frames = core.MaxGraphFrames // sit exactly on the count ceiling
		idSize = 1 << 20             // 1 MiB per frame ID -> a 1.0 GiB graph
	)
	pad := strings.Repeat("F", idSize)

	g := graph("frameidbomb", []core.Node{textNode("a", "x")}, nil)
	for i := range frames {
		g.Frames = append(g.Frames, core.Frame{ID: pad + itoa(i), Width: 10, Height: 10})
	}
	measured := core.ApproxGraphBytes(g, core.MaxGraphBytes)
	actual := graphJSONBytes(g)
	t.Logf("frame-ID bomb: ApproxGraphBytes=%d (ceiling %d), actual JSON=%d bytes",
		measured, core.MaxGraphBytes, actual)

	if err := newHarness(t).publish(t, g); err == nil {
		t.Errorf("FINDING: a %d-byte flow (%d frames, %d KiB of frame ID each) was stored — "+
			"the size walk measured it as %d bytes because it skips the frame ID",
			actual, frames, idSize>>10, measured)
	} else {
		t.Logf("refused at the save gate: %v", firstLine(err))
	}
}

// The trigger array is the other repeated sub-record. Only 32 of them fit, so
// the payload has to ride in the one field per trigger that nothing bounds.
func TestTriggerTypeBytes_AreCapped(t *testing.T) {
	const each = 4 << 20 // 4 MiB per trigger type -> a 128 MB graph

	g := graph("trigtypebomb", []core.Node{textNode("a", "x")}, nil)
	for range core.MaxGraphTriggers {
		g.Triggers = append(g.Triggers, core.GraphTrigger{Type: strings.Repeat("T", each)})
	}
	measured := core.ApproxGraphBytes(g, core.MaxGraphBytes)
	actual := graphJSONBytes(g)
	t.Logf("trigger-type bomb: ApproxGraphBytes=%d (ceiling %d), actual JSON=%d bytes",
		measured, core.MaxGraphBytes, actual)

	if err := newHarness(t).publish(t, g); err == nil {
		t.Errorf("FINDING: a %d-byte flow (%d triggers, %d MiB of type string each) was stored — "+
			"the size walk measured it as %d bytes because it skips the trigger type",
			actual, core.MaxGraphTriggers, each>>20, measured)
	} else {
		t.Logf("refused at the save gate: %v", firstLine(err))
	}
}

// Why the byte ceiling is worth having, and that the SUBMIT path applies it
// too rather than only the save gate: an unweighed graph rides in the run
// record and the worker re-reads it on every dispatch pass. Before frame IDs
// were charged, 200 MiB of them submitted clean and cost 280 MiB of run
// records for a single one-step run — 173,000x what the same flow costs bare,
// on every fire of a flow anyone can put a schedule on.
//
// The bare flow runs in the same case so a refusal that swallowed both would
// show up as a failure rather than as a pass.
func TestOversizedGraph_RunRecordCost(t *testing.T) {
	const (
		frames = 200
		idSize = 1 << 20 // 200 MiB of frame IDs
	)
	h := newHarness(t)

	bomb := graph("framerun", []core.Node{textNode("a", "x")}, nil)
	for i := range frames {
		bomb.Frames = append(bomb.Frames, core.Frame{ID: strings.Repeat("F", idSize) + itoa(i)})
	}
	bare := graph("framerunbare", []core.Node{textNode("a", "x")}, nil)

	for _, tc := range []struct {
		name string
		g    core.Graph
	}{{"frame-ID bomb", bomb}, {"the same flow bare", bare}} {
		start := time.Now()
		status, err := h.submit(tc.g, 60*time.Second)
		t.Logf("%s: graph %d bytes -> status=%v err=%v in %s, run records %d bytes",
			tc.name, graphJSONBytes(tc.g), status, firstLine(err),
			time.Since(start).Round(time.Millisecond), h.storedBytes(tc.g.ID))
		if status == statusHung {
			t.Errorf("run never reached a terminal status")
		}
	}
	if bytes := h.storedBytes(bare.ID); bytes <= 0 {
		t.Errorf("the bare flow stored %d bytes of run record — it should still run", bytes)
	}
	if bytes := h.storedBytes(bomb.ID); bytes > 0 {
		t.Errorf("FINDING: the oversized flow reached the run path and wrote %d bytes "+
			"of run record — the byte ceiling is a save-gate-only rule", bytes)
	}
}
