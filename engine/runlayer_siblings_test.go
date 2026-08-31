// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"
	"testing"

	"github.com/dazyflow/dazyflow/core"
)

// Every node in a layer runs before the layer is judged, so every node's result
// belongs in GraphResult.Nodes — including the siblings of a failing node.
//
// Merging and error-checking in a single pass returned on the first bad slot
// and dropped the rest. ExecutionLayers sorts a layer by node ID, so the
// survivors were decided alphabetically: here "a" (before the failing "b") was
// kept while "c" vanished, despite both having run and succeeded.
func TestRunLayer_KeepsSiblingResultsOfAFailedNode(t *testing.T) {
	reg := NewRegistry()
	okDrop := func(id string) NativeDrop {
		return NativeDrop{
			Manifest: core.Manifest{
				ID: id, Version: "1.0", Summary: "fixture.",
				Examples:       []core.ParamsExample{{Title: "default"}},
				ExecutionModel: core.ExecutionBatch, ProcessModel: core.ProcessLongLived,
				Outputs: []core.Port{{Port: "out"}},
			},
			Execute: func(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
				return core.Result{
					JobID: job.ID, Status: core.StatusOK,
					Output: map[string]core.Ref{"out": {Inline: "ran-" + job.NodeID}},
				}, nil
			},
		}
	}
	if err := reg.Register(okDrop("okfx")); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(NativeDrop{
		Manifest: core.Manifest{
			ID: "boomfx", Version: "1.0", Summary: "fixture.",
			Examples:       []core.ParamsExample{{Title: "default"}},
			ExecutionModel: core.ExecutionBatch, ProcessModel: core.ProcessLongLived,
			Outputs: []core.Port{{Port: "out"}},
		},
		Execute: func(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
			return core.Result{
				JobID: job.ID, Status: core.StatusError,
				Error: &core.JobError{Code: "boom", Message: "always fails"},
			}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	// One layer, three independent nodes. "b" fails; "a" sorts before it and
	// "c" after — the ordering that exposed the drop.
	g := core.Graph{
		ID: "siblings", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "a", Module: "okfx"},
			{ID: "b", Module: "boomfx"},
			{ID: "c", Module: "okfx"},
		},
	}

	eng := &Engine{Resolver: &NodeResolver{Native: reg}}
	res, err := eng.Run(context.Background(), g, nil)
	if err == nil {
		t.Fatal("Run should report the failing node")
	}
	if res.Status != core.StatusError {
		t.Errorf("status = %q, want error", res.Status)
	}
	for _, id := range []string{"a", "b", "c"} {
		if _, ok := res.Nodes[id]; !ok {
			t.Errorf("node %q missing from GraphResult.Nodes; it ran and its result was dropped", id)
		}
	}
	if got := res.Nodes["c"].Status; got != core.StatusOK {
		t.Errorf("c status = %q, want ok", got)
	}
}
