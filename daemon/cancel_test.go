// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/daemon"
)

// TestCancelGraphRun_FlipsRecords covers the happy path: a running
// graph with a mix of queued + awaiting node-records is cancelled in
// one call and every non-terminal record ends up marked Cancelled.
func TestCancelGraphRun_FlipsRecords(t *testing.T) {
	t.Parallel()
	h := newVisibilityHarness(t)
	ctx := context.Background()

	g := core.Graph{
		ID: "f1", Tenant: "t", Workspace: "ws",
		Visibility: core.VisibilityOrg,
		Nodes: []core.Node{
			{ID: "a", Module: "noop"},
			{ID: "b", Module: "noop"},
			{ID: "c", Module: "noop"},
		},
	}
	if _, err := h.svc.SaveGraph(ctx, h.alice, g); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Seed a fake live run: a graph-record + three node-records in
	// queued / running / awaiting respectively.
	payload, _ := json.Marshal(g)
	if err := h.svc.Jobs.Enqueue(ctx, core.JobRecord{
		ID:           "run-1",
		Kind:         core.JobKindGraph,
		GraphID:      "f1",
		NodeID:       "*",
		Tenant:       "t",
		Workspace:    "ws",
		Status:       core.JobStatusRunning,
		GraphPayload: payload,
		Job:          core.Job{ID: "run-1", GraphID: "f1"},
	}); err != nil {
		t.Fatalf("enqueue graph rec: %v", err)
	}
	for _, n := range []struct {
		id     string
		status core.JobStatus
	}{
		{"a", core.JobStatusQueued},
		{"b", core.JobStatusRunning},
		{"c", core.JobStatusAwaiting},
	} {
		if err := h.svc.Jobs.Enqueue(ctx, core.JobRecord{
			ID:         daemon.NodeJobID("run-1", n.id),
			Kind:       core.JobKindNode,
			GraphRunID: "run-1",
			GraphID:    "f1",
			NodeID:     n.id,
			Tenant:     "t",
			Workspace:  "ws",
			Status:     n.status,
			Job:        core.Job{GraphID: "f1", NodeID: n.id},
		}); err != nil {
			t.Fatalf("enqueue node %s: %v", n.id, err)
		}
	}

	if err := h.svc.CancelGraphRun(ctx, h.alice, "run-1", "user clicked cancel"); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	// Every record — graph + the three nodes — must now be Cancelled.
	for _, id := range []string{
		"run-1",
		daemon.NodeJobID("run-1", "a"),
		daemon.NodeJobID("run-1", "b"),
		daemon.NodeJobID("run-1", "c"),
	} {
		rec, err := h.svc.Jobs.Get(ctx, id)
		if err != nil {
			t.Fatalf("get %s: %v", id, err)
		}
		if rec.Status != core.JobStatusCancelled {
			t.Errorf("%s status = %q, want cancelled", id, rec.Status)
		}
	}
}

// TestCancelGraphRun_AlreadyTerminal proves that a finished run can't
// be cancelled — the user gets ErrConflict rather than a silent
// re-cancel that would re-publish a Terminal event.
func TestCancelGraphRun_AlreadyTerminal(t *testing.T) {
	t.Parallel()
	h := newVisibilityHarness(t)
	ctx := context.Background()

	g := core.Graph{ID: "f1", Tenant: "t", Workspace: "ws", Visibility: core.VisibilityOrg}
	if _, err := h.svc.SaveGraph(ctx, h.alice, g); err != nil {
		t.Fatalf("save: %v", err)
	}
	payload, _ := json.Marshal(g)
	if err := h.svc.Jobs.Enqueue(ctx, core.JobRecord{
		ID: "run-1", Kind: core.JobKindGraph, GraphID: "f1", NodeID: "*",
		Tenant: "t", Workspace: "ws", Status: core.JobStatusRunning,
		GraphPayload: payload,
	}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := h.svc.Jobs.Complete(ctx, "run-1", core.JobStatusSucceeded, &core.Result{Status: core.StatusOK}); err != nil {
		t.Fatalf("complete: %v", err)
	}

	err := h.svc.CancelGraphRun(ctx, h.alice, "run-1", "")
	if err == nil {
		t.Fatal("cancel succeeded against terminal run")
	}
	if !errors.Is(err, core.ErrConflict) {
		t.Errorf("err = %v, want ErrConflict", err)
	}
}

// TestCancelGraphRun_RequiresGraphRun confirms the principal needs
// graph:run on the underlying graph — viewers can't cancel.
func TestCancelGraphRun_RequiresGraphRun(t *testing.T) {
	t.Parallel()
	h := newVisibilityHarness(t)
	ctx := context.Background()

	// Bob is a viewer in 't' but only at the workspace level — give
	// him no run permission for this test.
	viewer := core.Principal{
		Subject: "viewer", Tenant: "t", Workspace: "ws",
		Roles: []core.Role{{Name: "viewer", Permissions: []core.Permission{}}},
	}

	g := core.Graph{ID: "f1", Tenant: "t", Workspace: "ws", Visibility: core.VisibilityOrg}
	if _, err := h.svc.SaveGraph(ctx, h.alice, g); err != nil {
		t.Fatalf("save: %v", err)
	}
	payload, _ := json.Marshal(g)
	if err := h.svc.Jobs.Enqueue(ctx, core.JobRecord{
		ID: "run-1", Kind: core.JobKindGraph, GraphID: "f1", NodeID: "*",
		Tenant: "t", Workspace: "ws", Status: core.JobStatusRunning,
		GraphPayload: payload,
	}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	err := h.svc.CancelGraphRun(ctx, viewer, "run-1", "")
	if err == nil {
		t.Fatal("viewer cancelled the run")
	}
	if !errors.Is(err, core.ErrUnauthorized) {
		t.Errorf("err = %v, want ErrUnauthorized", err)
	}
}
