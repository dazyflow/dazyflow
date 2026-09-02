// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/daemon"
)

func TestFSSandbox_ScratchLifecycle(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	sb, err := daemon.NewFSSandbox(base)
	if err != nil {
		t.Fatalf("NewFSSandbox: %v", err)
	}
	sp, ok := any(sb).(core.ScratchProvider)
	if !ok {
		t.Fatal("FSSandbox does not implement core.ScratchProvider")
	}

	scratch, err := sp.ScratchRoot("acme", "ws", "run123")
	if err != nil {
		t.Fatalf("ScratchRoot: %v", err)
	}
	// Lives under the (tenant, workspace) subtree so it's quota-counted.
	wantPrefix := filepath.Join(base, "acme", "ws", ".scratch", "run123")
	if scratch != wantPrefix {
		// EvalSymlinks may canonicalize; compare suffix instead of exact.
		if filepath.Base(scratch) != "run123" {
			t.Errorf("scratch = %q, want under %q", scratch, wantPrefix)
		}
	}
	if fi, err := os.Stat(scratch); err != nil || !fi.IsDir() {
		t.Fatalf("scratch dir not created: %v", err)
	}

	// Quota counts scratch contents (walkUsage walks the tenant subtree).
	if err := os.WriteFile(filepath.Join(scratch, "blob"), make([]byte, 500), 0o644); err != nil {
		t.Fatal(err)
	}
	q, _ := daemon.NewFSQuota(base, map[string]int64{"acme": 1 << 20})
	q.SetCacheTTL(0)
	if used, _ := q.Used("acme"); used != 500 {
		t.Errorf("Used = %d, want 500 (scratch counts against quota)", used)
	}

	// RemoveScratch reclaims it and frees the quota.
	if err := sp.RemoveScratch("acme", "ws", "run123"); err != nil {
		t.Fatalf("RemoveScratch: %v", err)
	}
	if _, err := os.Stat(scratch); !os.IsNotExist(err) {
		t.Errorf("scratch dir still present after RemoveScratch: %v", err)
	}
	if used, _ := q.Used("acme"); used != 0 {
		t.Errorf("Used = %d after reclaim, want 0", used)
	}

	// Idempotent: reclaiming again (or a run that never created scratch).
	if err := sp.RemoveScratch("acme", "ws", "run123"); err != nil {
		t.Errorf("second RemoveScratch should be a no-op, got %v", err)
	}
	if err := sp.RemoveScratch("acme", "ws", "never-ran"); err != nil {
		t.Errorf("RemoveScratch on absent run: %v", err)
	}
}

func TestFSSandbox_ScratchRejectsUnsafeRunID(t *testing.T) {
	t.Parallel()
	sb, _ := daemon.NewFSSandbox(t.TempDir())
	sp := any(sb).(core.ScratchProvider)
	// Each "/"-separated segment must be a safe identifier; traversal,
	// empty segments, and leading dots are rejected. (A bare "a/b" is now
	// accepted as a nested loop-item scratch path — see the next test.)
	for _, bad := range []string{"..", "../escape", "a/../b", "a//b", "/abs", "a/..", ""} {
		if _, err := sp.ScratchRoot("acme", "ws", bad); err == nil {
			t.Errorf("ScratchRoot accepted unsafe run id %q", bad)
		}
	}
}

// TestFSSandbox_ScratchNestedRunID covers the loop-body case: a per-item
// scratch path "<parentRunID>/iN" is accepted and nests under the parent
// run's scratch, so reclaiming the parent removes every item subdir.
func TestFSSandbox_ScratchNestedRunID(t *testing.T) {
	t.Parallel()
	sb, _ := daemon.NewFSSandbox(t.TempDir())
	sp := any(sb).(core.ScratchProvider)
	a, err := sp.ScratchRoot("acme", "ws", "run123/i0")
	if err != nil {
		t.Fatalf("nested scratch i0: %v", err)
	}
	b, err := sp.ScratchRoot("acme", "ws", "run123/i1")
	if err != nil {
		t.Fatalf("nested scratch i1: %v", err)
	}
	if a == b {
		t.Fatalf("concurrent iterations got the same scratch dir %q", a)
	}
	if _, err := os.Stat(a); err != nil {
		t.Errorf("item scratch i0 missing: %v", err)
	}
	// Reclaiming the parent run removes both item subdirs.
	if err := sp.RemoveScratch("acme", "ws", "run123"); err != nil {
		t.Fatalf("RemoveScratch parent: %v", err)
	}
	if _, err := os.Stat(a); !os.IsNotExist(err) {
		t.Errorf("item scratch i0 still present after parent reclaim: %v", err)
	}
}

// TestScratch_ReclaimedOnGraphCompletion runs a real graph end to end
// (read a seeded workspace file, write it to scratch://) and asserts the
// run's scratch directory is gone once the run reaches terminal — the
// dispatcher's reclamation path.
func TestScratch_ReclaimedOnGraphCompletion(t *testing.T) {
	t.Parallel()
	h := newQuotaHarness(t, nil) // no quota limits needed here
	ctx := context.Background()

	root, _ := h.sandbox.Root("acme", "ws1")
	if err := os.WriteFile(filepath.Join(root, "seed.txt"), []byte("data"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	g := core.Graph{
		ID: "scratchflow", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{
			{ID: "rd", Module: "file_read", Params: map[string]any{"path": "seed.txt"}},
			{ID: "wr", Module: "file_write", Params: map[string]any{"path": "scratch://out.txt"}},
		},
		Edges: []core.Edge{{From: "rd", FromPort: "out", To: "wr", ToPort: "in"}},
	}
	runID, err := h.svc.SubmitGraph(ctx, h.principal, g)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	term := waitForTerminalEvent(t, h.bus, h.jobs, runID, 5*time.Second)
	if term.Status != core.JobStatusSucceeded {
		t.Fatalf("run status = %q (want succeeded); err=%+v", term.Status, term.Error)
	}

	// Scratch lives at <workspaceRoot>/.scratch/<runID>; it must be gone.
	scratchPath := filepath.Join(root, ".scratch", runID)
	if _, err := os.Stat(scratchPath); !os.IsNotExist(err) {
		t.Errorf("scratch dir %s not reclaimed after completion: %v", scratchPath, err)
	}
}
