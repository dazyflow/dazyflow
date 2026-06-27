// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon_test

import (
	"context"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/auth"
	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/daemon"
	_ "git.sr.ht/~klahr/dazyflow/drops"
	"git.sr.ht/~klahr/dazyflow/engine"
	"git.sr.ht/~klahr/dazyflow/engine/jobstore"
	"git.sr.ht/~klahr/dazyflow/workspace"
)

// TestPassthrough_ThreadsValueThroughNode confirms the universal value
// passthrough: a value wired into a node's reserved "pass" input is re-emitted
// unchanged on that node's "pass" output when it runs — without the drop
// itself doing anything. Here a source emits a value into sleep.pass; sleep
// computes nothing useful, yet its result carries the threaded value.
func TestPassthrough_ThreadsValueThroughNode(t *testing.T) {
	ks := auth.NewMemKeyStore()
	role := core.Role{Name: "editor", Permissions: []core.Permission{
		core.PermGraphRun, core.PermGraphEdit, core.PermGraphAdmin,
	}}
	_, _, _ = auth.IssueAPIKey(ks, t.Context(), "k", "t", "ws", "u", []core.Role{role}, nil)
	p := core.Principal{Subject: "u", Tenant: "t", Workspace: "ws", Roles: []core.Role{role}}

	ws, _ := workspace.OpenFS("")

	// A local registry: a fixture source that emits a known value, plus the
	// real default drops (we route through sleep as the pass-through carrier).
	reg := engine.NewRegistry()
	const threaded = "correlation-id-99"
	_ = reg.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:       "value_source",
			Summary:  "Test fixture that emits a constant value on out.",
			Examples: []core.ParamsExample{{Title: "default"}},
			Outputs:  []core.Port{{Port: "out"}},
		},
		Execute: func(_ context.Context, j core.Job, _ chan<- core.Progress) (core.Result, error) {
			return core.Result{Status: core.StatusOK, Output: map[string]core.Ref{
				"out": {MIME: "text/plain", Inline: threaded},
			}}, nil
		},
	})
	for id, m := range engine.Default.Manifests() {
		captured, _ := engine.Default.Get(id)
		_ = reg.Register(engine.NativeDrop{
			Manifest: m,
			Execute: func(ctx context.Context, j core.Job, pr chan<- core.Progress) (core.Result, error) {
				return captured.Execute(ctx, j, pr)
			},
		})
	}

	eng := &engine.Engine{Resolver: &engine.NodeResolver{Native: reg}}
	bus := daemon.NewMemoryBus()
	jobs := jobstore.NewMemory()
	svc := &daemon.Service{
		Auth:       auth.Chain{&auth.APIKeyAuthenticator{Store: ks}},
		Workspaces: daemon.MapWorkspaces{"t/ws": ws},
		Jobs:       jobs,
		Engine:     eng,
		Bus:        bus,
	}
	wctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := daemon.NewWorker(daemon.WorkerConfig{
		ID: "w", PollInterval: 5 * time.Millisecond, MaxRetries: 1,
	}, jobs, eng, bus)
	go func() { _ = w.Run(wctx) }()

	g := core.Graph{
		ID: "passflow", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "src", Module: "value_source"},
			{ID: "carry", Module: "delay", Params: map[string]any{"ms": 1}},
		},
		Edges: []core.Edge{
			// Wire the value straight into the carrier's pass input.
			{From: "src", FromPort: "out", To: "carry", ToPort: core.PassPort},
		},
	}

	runID, err := svc.SubmitGraph(t.Context(), p, g)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	terminal := waitForTerminalEvent(t, bus, jobs, runID, 5*time.Second)
	if terminal.Status != core.JobStatusSucceeded {
		t.Fatalf("status=%q (%+v)", terminal.Status, terminal.Error)
	}

	rec, _ := jobs.Get(t.Context(), daemon.NodeJobID(runID, "carry"))
	if rec.Result == nil || rec.Result.Output == nil {
		t.Fatalf("carry produced no output")
	}
	passed, ok := rec.Result.Output[core.PassPort]
	if !ok {
		t.Fatalf("carry did not emit a pass output; got ports %v", keysOf(rec.Result.Output))
	}
	if passed.Inline != threaded {
		t.Errorf("threaded value = %v, want %q", passed.Inline, threaded)
	}
}

func keysOf(m map[string]core.Ref) []string {
	out := []string{}
	for k := range m {
		out = append(out, k)
	}
	return out
}
