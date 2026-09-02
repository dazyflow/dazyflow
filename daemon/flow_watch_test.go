// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
)

// TestSaveGraph_PublishesFlowUpdated covers the live flow-watch publish
// point: every SaveGraph (the path the web editor, the MCP server, and the
// API all funnel through) emits a FlowUpdated event on the flow's bus key so
// an open editor can reflect the change. The Commit lets a client suppress
// the echo of its own save; the Author names who wrote it.
func TestSaveGraph_PublishesFlowUpdated(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)
	ctx := t.Context()
	p, err := h.svc.Authenticate(ctx, h.token)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}

	// Subscribe BEFORE the save so the fan-out reaches us (subscribers
	// don't replay history).
	events, cancel := h.bus.Subscribe(flowBusKey("t", "ws", "watchme"))
	defer cancel()

	commit, err := h.svc.SaveGraph(ctx, p, core.Graph{
		ID: "watchme", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{{ID: "a", Module: "noop"}},
	})
	if err != nil {
		t.Fatalf("save graph: %v", err)
	}

	select {
	case ev := <-events:
		if ev.FlowUpdated == nil {
			t.Fatalf("event = %+v, want FlowUpdated set", ev)
		}
		if got, want := ev.FlowUpdated.FlowID, "t/ws/watchme"; got != want {
			t.Errorf("FlowID = %q, want %q", got, want)
		}
		if ev.FlowUpdated.Commit != commit {
			t.Errorf("Commit = %q, want %q (the save's commit)", ev.FlowUpdated.Commit, commit)
		}
		if ev.FlowUpdated.Author != p.Subject {
			t.Errorf("Author = %q, want %q", ev.FlowUpdated.Author, p.Subject)
		}
		if ev.FlowUpdated.Autosave {
			t.Errorf("Autosave = true, want false for an explicit SaveGraph")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for FlowUpdated event")
	}
}

// TestSaveGraphCoalescing_FlagsAutosave verifies the autosave path marks the
// event so a client can tell an editor autosave burst from an explicit
// checkpoint.
func TestSaveGraphCoalescing_FlagsAutosave(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)
	ctx := t.Context()
	p, err := h.svc.Authenticate(ctx, h.token)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	events, cancel := h.bus.Subscribe(flowBusKey("t", "ws", "auto"))
	defer cancel()

	if _, err := h.svc.SaveGraphCoalescing(ctx, p, core.Graph{
		ID: "auto", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{{ID: "a", Module: "noop"}},
	}); err != nil {
		t.Fatalf("save coalescing: %v", err)
	}

	select {
	case ev := <-events:
		if ev.FlowUpdated == nil || !ev.FlowUpdated.Autosave {
			t.Fatalf("event = %+v, want FlowUpdated with Autosave=true", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for FlowUpdated event")
	}
}
