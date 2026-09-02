// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"strings"
	"testing"

	"github.com/dazyflow/dazyflow/core"
)

// Every step stores its own copy of what it emitted, so one payload
// threaded down a long chain becomes payload × steps of run state. The
// meter is what stops the run rather than the store filling up.
func TestRunStateMeter_ChargesPerRun(t *testing.T) {
	defer core.SetMaxRunStateBytes(100)()
	var m runStateMeter

	if total, ok := m.charge("run-1", 60); !ok || total != 60 {
		t.Fatalf("first charge = (%d, %v), want (60, true)", total, ok)
	}
	if total, ok := m.charge("run-1", 30); !ok || total != 90 {
		t.Fatalf("second charge = (%d, %v), want (90, true)", total, ok)
	}
	if total, ok := m.charge("run-1", 30); ok {
		t.Errorf("third charge = (%d, %v), want refused past 100", total, ok)
	}
	// Runs are independent: one flow's big payload doesn't fail another's.
	if _, ok := m.charge("run-2", 60); !ok {
		t.Error("a second run was charged for the first run's bytes")
	}
}

func TestRunStateMeter_DisabledByZeroLimit(t *testing.T) {
	defer core.SetMaxRunStateBytes(0)()
	var m runStateMeter
	if _, ok := m.charge("run", 1<<40); !ok {
		t.Error("a zero limit still refused a charge")
	}
}

// The window is bounded so the meter can't grow with the run count; an
// evicted run simply starts counting again.
func TestRunStateMeter_WindowIsBounded(t *testing.T) {
	defer core.SetMaxRunStateBytes(1 << 30)()
	var m runStateMeter
	for i := 0; i < maxMeteredRuns*2; i++ {
		m.charge(string(rune('a'+i%26))+strings.Repeat("x", i), 1)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.bytes) > maxMeteredRuns {
		t.Errorf("meter holds %d runs, want at most %d", len(m.bytes), maxMeteredRuns)
	}
}

func TestResultStateBytes_SumsOutputs(t *testing.T) {
	got := resultStateBytes(&core.Result{Output: map[string]core.Ref{
		"a": {Inline: "12345"},
		"b": {Inline: []any{"12", "34"}},
		"c": {Ref: "blob://elsewhere"}, // out-of-line: not in the record
	}})
	if got != 9 {
		t.Errorf("resultStateBytes = %d, want 9", got)
	}
	if resultStateBytes(nil) != 0 {
		t.Error("resultStateBytes(nil) should be 0")
	}
}
