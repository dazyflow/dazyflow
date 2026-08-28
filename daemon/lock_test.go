// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon_test

import (
	"context"
	"errors"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

// TestSaveGraph_LockedByActiveRun covers the rule that SaveGraph
// refuses to overwrite a flow while any non-terminal graph-record
// exists for it. Each non-terminal status (queued, running, awaiting)
// is checked independently so a regression in the helper's status loop
// is caught.
func TestSaveGraph_LockedByActiveRun(t *testing.T) {
	for _, status := range []core.JobStatus{
		core.JobStatusQueued,
		core.JobStatusRunning,
		core.JobStatusAwaiting,
	} {
		t.Run(string(status), func(t *testing.T) {
			h := newVisibilityHarness(t)
			ctx := context.Background()

			// Seed an org-visible flow so the second SaveGraph hits the
			// update path (the lock check only runs there).
			if _, err := h.svc.SaveGraph(ctx, h.alice, core.Graph{
				ID: "f1", Tenant: "t", Workspace: "ws",
				Visibility: core.VisibilityOrg,
			}); err != nil {
				t.Fatalf("initial save: %v", err)
			}

			// Plant a graph-record in the target status, bypassing
			// SubmitGraph so we can pin the status precisely.
			if err := h.svc.Jobs.Enqueue(ctx, core.JobRecord{
				ID:        "run-1",
				Kind:      core.JobKindGraph,
				GraphID:   "f1",
				NodeID:    "*",
				Tenant:    "t",
				Workspace: "ws",
				Status:    status,
				Job:       core.Job{ID: "run-1", GraphID: "f1"},
			}); err != nil {
				t.Fatalf("enqueue active run: %v", err)
			}

			_, err := h.svc.SaveGraph(ctx, h.alice, core.Graph{
				ID: "f1", Tenant: "t", Workspace: "ws",
				Visibility: core.VisibilityOrg,
			})
			if err == nil {
				t.Fatal("save succeeded while run was active; want ErrConflict")
			}
			if !errors.Is(err, core.ErrConflict) {
				t.Errorf("err = %v, want ErrConflict", err)
			}
		})
	}
}

// TestSaveGraph_UnlockedAfterTerminal verifies that the lock releases
// once the active run reaches a terminal state — otherwise the flow
// would be wedged read-only forever.
func TestSaveGraph_UnlockedAfterTerminal(t *testing.T) {
	h := newVisibilityHarness(t)
	ctx := context.Background()

	if _, err := h.svc.SaveGraph(ctx, h.alice, core.Graph{
		ID: "f1", Tenant: "t", Workspace: "ws",
		Visibility: core.VisibilityOrg,
	}); err != nil {
		t.Fatalf("initial save: %v", err)
	}
	if err := h.svc.Jobs.Enqueue(ctx, core.JobRecord{
		ID:        "run-1",
		Kind:      core.JobKindGraph,
		GraphID:   "f1",
		NodeID:    "*",
		Tenant:    "t",
		Workspace: "ws",
		Status:    core.JobStatusRunning,
		Job:       core.Job{ID: "run-1", GraphID: "f1"},
	}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := h.svc.Jobs.Complete(ctx, "run-1", core.JobStatusSucceeded, &core.Result{Status: core.StatusOK}); err != nil {
		t.Fatalf("complete run: %v", err)
	}

	// Vary a benign field so the workspace store has a real diff to
	// commit (it rejects empty commits with "clean working tree").
	if _, err := h.svc.SaveGraph(ctx, h.alice, core.Graph{
		ID: "f1", Tenant: "t", Workspace: "ws",
		Visibility:  core.VisibilityOrg,
		Description: "edited after run",
	}); err != nil {
		t.Fatalf("save after terminal: %v", err)
	}
}

// TestSaveGraph_CreateIgnoresLock covers the create branch — a brand
// new graph ID can't be "locked" because there's no prior to load. The
// helper should never short-circuit a create.
func TestSaveGraph_CreateIgnoresLock(t *testing.T) {
	h := newVisibilityHarness(t)
	ctx := context.Background()

	// Plant an active run for a DIFFERENT graph to prove the lock is
	// scoped per-graphID, not per-workspace.
	if _, err := h.svc.SaveGraph(ctx, h.alice, core.Graph{
		ID: "other", Tenant: "t", Workspace: "ws",
		Visibility: core.VisibilityOrg,
	}); err != nil {
		t.Fatalf("seed other: %v", err)
	}
	if err := h.svc.Jobs.Enqueue(ctx, core.JobRecord{
		ID:        "run-other",
		Kind:      core.JobKindGraph,
		GraphID:   "other",
		NodeID:    "*",
		Tenant:    "t",
		Workspace: "ws",
		Status:    core.JobStatusRunning,
		Job:       core.Job{ID: "run-other", GraphID: "other"},
	}); err != nil {
		t.Fatalf("enqueue other: %v", err)
	}

	if _, err := h.svc.SaveGraph(ctx, h.alice, core.Graph{
		ID: "fresh", Tenant: "t", Workspace: "ws",
		Visibility: core.VisibilityOrg,
	}); err != nil {
		t.Fatalf("create while another flow is running: %v", err)
	}
}
