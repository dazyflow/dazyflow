// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"
	"testing"

	"github.com/dazyflow/dazyflow/core"
)

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
