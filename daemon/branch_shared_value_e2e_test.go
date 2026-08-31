// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/daemon"
	_ "github.com/dazyflow/dazyflow/drops"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/engine/jobstore"
	"github.com/dazyflow/dazyflow/workspace"
)

// TestBranch_ValueWireCannotRunTheUntakenSide is the double-send regression.
//
// The shape is the one people actually build: an If that mails one thing on
// Yes and another on No, where BOTH send steps take their recipient from an
// Email step sitting off to the side. That address wire is live for both
// branches, and it used to be enough on its own to make the untaken send step
// runnable — dispatch treated the If's unemitted `else` port as "no comment"
// rather than "not down here", so any other live wire ran the branch nobody
// chose and two mails went out.
//
// Note the untaken side is checked TWO steps deep: the skip has to keep
// travelling past a step that itself has a live wire into it, or the leak
// simply moves one node downstream.
func TestBranch_ValueWireCannotRunTheUntakenSide(t *testing.T) {
	var sentYes, sentNo, followUp atomic.Int32

	reg := engine.NewRegistry()
	sender := func(id string, counter *atomic.Int32) {
		_ = reg.Register(engine.NativeDrop{
			Manifest: core.Manifest{
				ID:       id,
				Summary:  "Test fixture standing in for a send-email step.",
				Examples: []core.ParamsExample{{Title: "default"}},
				Inputs:   []core.Port{{Port: "body"}, {Port: "to"}},
				Outputs:  []core.Port{{Port: "out"}},
			},
			Execute: func(context.Context, core.Job, chan<- core.Progress) (core.Result, error) {
				counter.Add(1)
				return core.Result{Status: core.StatusOK,
					Output: map[string]core.Ref{"out": {Inline: "sent"}}}, nil
			},
		})
	}
	sender("test_send_yes", &sentYes)
	sender("test_send_no", &sentNo)
	sender("test_follow_up", &followUp)
	// Bring the real drops (if, text, email) in alongside the fixtures.
	for id, m := range engine.Default.Manifests() {
		transport, _ := engine.Default.Get(id)
		_ = reg.Register(engine.NativeDrop{
			Manifest: m,
			Execute: func(ctx context.Context, j core.Job, p chan<- core.Progress) (core.Result, error) {
				return transport.Execute(ctx, j, p)
			},
		})
	}

	ks := auth.NewMemKeyStore()
	role := core.Role{Name: "editor", Permissions: []core.Permission{
		core.PermGraphRun, core.PermGraphEdit, core.PermGraphAdmin,
	}}
	_, _, _ = auth.IssueAPIKey(ks, t.Context(), "k", "t", "ws", "u", []core.Role{role}, nil)
	p := core.Principal{Subject: "u", Tenant: "t", Workspace: "ws", Roles: []core.Role{role}}

	ws, _ := workspace.OpenFS("")
	jobs := jobstore.NewMemory()
	bus := daemon.NewMemoryBus()
	eng := &engine.Engine{Resolver: &engine.NodeResolver{Native: reg}}
	svc := &daemon.Service{
		Auth:       auth.Chain{&auth.APIKeyAuthenticator{Store: ks}},
		Workspaces: daemon.MapWorkspaces{"t/ws": ws},
		Jobs:       jobs,
		Engine:     eng,
		Bus:        bus,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := daemon.NewWorker(daemon.WorkerConfig{
		ID: "w", PollInterval: 5 * time.Millisecond, MaxRetries: 1,
	}, jobs, eng, bus)
	go func() { _ = w.Run(ctx) }()

	g := core.Graph{
		ID: "branch-shared-value", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "subject", Module: "text", Params: map[string]any{"text": "urgent"}},
			{ID: "check", Module: "if", Params: map[string]any{"op": "equals", "B": "urgent"}},
			// The address step: no incoming wire, so it runs as a root and its
			// output is live for both branches.
			{ID: "addr", Module: "email", Params: map[string]any{"email": "ada@acme.com"}},
			{ID: "yes", Module: "test_send_yes"},
			{ID: "no", Module: "test_send_no"},
			{ID: "after_no", Module: "test_follow_up"},
		},
		Edges: []core.Edge{
			{From: "subject", FromPort: "out", To: "check", ToPort: "A"},
			{From: "check", FromPort: "then", To: "yes", ToPort: "body"},
			{From: "check", FromPort: "else", To: "no", ToPort: "body"},
			// One address step feeding the To pin of both branches.
			{From: "addr", FromPort: "out", To: "yes", ToPort: "to"},
			{From: "addr", FromPort: "out", To: "no", ToPort: "to"},
			// A step past the untaken side, itself holding a live wire.
			{From: "no", FromPort: "out", To: "after_no", ToPort: "body"},
			{From: "addr", FromPort: "out", To: "after_no", ToPort: "to"},
		},
	}

	runID, err := svc.SubmitGraph(t.Context(), p, g)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	terminal := waitForTerminalEvent(t, bus, jobs, runID, 5*time.Second)
	if terminal.Status != core.JobStatusSucceeded {
		t.Fatalf("run status=%q (%+v)", terminal.Status, terminal.Error)
	}

	if got := sentYes.Load(); got != 1 {
		t.Errorf("taken branch ran %d times, want exactly 1", got)
	}
	if got := sentNo.Load(); got != 0 {
		t.Errorf("untaken branch ran %d times, want 0 — the address wire ran the branch the If declined", got)
	}
	if got := followUp.Load(); got != 0 {
		t.Errorf("step after the untaken branch ran %d times, want 0 — the skip stopped travelling", got)
	}

	for _, c := range []struct {
		node string
		want core.JobStatus
	}{
		{"yes", core.JobStatusSucceeded},
		{"no", core.JobStatusSkipped},
		{"after_no", core.JobStatusSkipped},
		// The address step still runs on its own: it is a root, and nothing
		// about branch routing should stop it.
		{"addr", core.JobStatusSucceeded},
	} {
		rec, err := jobs.Get(t.Context(), daemon.NodeJobID(runID, c.node))
		if err != nil {
			t.Fatalf("get %s: %v", c.node, err)
		}
		if rec.Status != c.want {
			t.Errorf("node %s = %q, want %q", c.node, rec.Status, c.want)
		}
	}
}
