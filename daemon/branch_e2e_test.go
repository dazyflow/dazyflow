package daemon_test

import (
	"context"
	"testing"
	"time"

	"git.sr.ht/~klahr/hazy-flow/auth"
	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/daemon"
	"git.sr.ht/~klahr/hazy-flow/engine"
	"git.sr.ht/~klahr/hazy-flow/engine/jobstore"
	_ "git.sr.ht/~klahr/hazy-flow/integrations"
	"git.sr.ht/~klahr/hazy-flow/workspace"
)

// TestBranch_RoutesThroughDispatch confirms the engine's dormant-edge
// fix lets only the taken branch run. The graph forks into "then" and
// "else"; the worker should run exactly one of them based on the
// condition outcome.
func TestBranch_RoutesThroughDispatch(t *testing.T) {
	ks := auth.NewMemKeyStore()
	role := core.Role{Name: "editor", Permissions: []core.Permission{
		core.PermGraphRun, core.PermGraphEdit, core.PermGraphAdmin,
	}}
	_, _, _ = auth.IssueAPIKey(ks, t.Context(), "k", "t", "ws", "u", []core.Role{role}, nil)
	p := core.Principal{Subject: "u", Tenant: "t", Workspace: "ws", Roles: []core.Role{role}}

	ws, _ := workspace.OpenFS("")
	jobs := jobstore.NewMemory()
	bus := daemon.NewMemoryBus()
	eng := &engine.Engine{Resolver: &engine.NodeResolver{Native: engine.Default}}
	svc := &daemon.Service{
		Auth:       auth.Chain{&auth.APIKeyAuthenticator{Store: ks}},
		Workspaces: daemon.MapWorkspaces{"t/ws": ws},
		Jobs:       jobs,
		Engine:     eng,
		Bus:        bus,
	}
	wctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	w := daemon.NewWorker(daemon.WorkerConfig{
		ID: "w", PollInterval: 5 * time.Millisecond,
		MaxRetries: 1,
	}, jobs, eng, bus)
	go func() { _ = w.Run(wctx) }()

	cases := []struct {
		name      string
		value     float64
		threshold float64
		want      string // "yes" or "no" depending on which branch took it
	}{
		{name: "high value goes to yes", value: 50000, threshold: 10000, want: "yes"},
		{name: "low value goes to no", value: 500, threshold: 10000, want: "no"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := core.Graph{
				ID: "branch-" + c.name, Tenant: "t", Workspace: "ws",
				Nodes: []core.Node{
					{ID: "source", Module: "sleep", Params: map[string]any{"ms": 1}},
					{ID: "decide", Module: "branch", Params: map[string]any{
						"condition": map[string]any{"op": "greater_than", "value": c.threshold},
					}},
					{ID: "yes", Module: "sleep", Params: map[string]any{"ms": 1}},
					{ID: "no", Module: "sleep", Params: map[string]any{"ms": 1}},
				},
				Edges: []core.Edge{
					// source feeds the value (its inline content) to branch.in
					{From: "source", FromPort: "out", To: "decide", ToPort: "in"},
					{From: "decide", FromPort: "then", To: "yes", ToPort: "in"},
					{From: "decide", FromPort: "else", To: "no", ToPort: "in"},
				},
			}
			// Override the source's output by hand: sleep doesn't actually
			// emit a numeric value. Use a custom registered module.
			// (Simpler approach: just check the branch node itself.)

			// Replace source with a numeric emitter for this test.
			reg := engine.NewRegistry()
			_ = reg.Register(engine.NativeDrop{
				Manifest: core.Manifest{
					ID:      "numeric_source",
					Outputs: []core.Port{{Port: "out"}},
				},
				Execute: func(_ context.Context, j core.Job, _ chan<- core.Progress) (core.Result, error) {
					return core.Result{Status: core.StatusOK, Output: map[string]core.Ref{
						"out": {Inline: c.value},
					}}, nil
				},
			})
			// Bring in the other modules from the default registry.
			for id, m := range engine.Default.Manifests() {
				m := m
				nt, _ := engine.Default.Get(id)
				captured := nt
				_ = reg.Register(engine.NativeDrop{
					Manifest: m,
					Execute: func(ctx context.Context, j core.Job, p chan<- core.Progress) (core.Result, error) {
						return captured.Execute(ctx, j, p)
					},
				})
			}

			localEng := &engine.Engine{Resolver: &engine.NodeResolver{Native: reg}}
			localBus := daemon.NewMemoryBus()
			localJobs := jobstore.NewMemory()
			localSvc := &daemon.Service{
				Auth:       svc.Auth,
				Workspaces: svc.Workspaces,
				Jobs:       localJobs,
				Engine:     localEng,
				Bus:        localBus,
			}
			localCtx, localCancel := context.WithCancel(context.Background())
			defer localCancel()
			localW := daemon.NewWorker(daemon.WorkerConfig{
				ID: "wl", PollInterval: 5 * time.Millisecond, MaxRetries: 1,
			}, localJobs, localEng, localBus)
			go func() { _ = localW.Run(localCtx) }()

			g.Nodes[0].Module = "numeric_source"

			runID, err := localSvc.SubmitGraph(t.Context(), p, g)
			if err != nil {
				t.Fatalf("Submit: %v", err)
			}
			terminal := waitForTerminalEvent(t, localBus, localJobs, runID, 5*time.Second)
			if terminal.Status != core.JobStatusSucceeded {
				t.Fatalf("status=%q (%+v)", terminal.Status, terminal.Error)
			}

			yesRec, _ := localJobs.Get(t.Context(), daemon.NodeJobID(runID, "yes"))
			noRec, _ := localJobs.Get(t.Context(), daemon.NodeJobID(runID, "no"))

			if c.want == "yes" {
				if yesRec.Status != core.JobStatusSucceeded {
					t.Errorf("yes should have succeeded; got %q", yesRec.Status)
				}
				if noRec.Status != core.JobStatusSkipped {
					t.Errorf("no should be skipped; got %q", noRec.Status)
				}
			} else {
				if noRec.Status != core.JobStatusSucceeded {
					t.Errorf("no should have succeeded; got %q", noRec.Status)
				}
				if yesRec.Status != core.JobStatusSkipped {
					t.Errorf("yes should be skipped; got %q", yesRec.Status)
				}
			}
		})
	}
}
