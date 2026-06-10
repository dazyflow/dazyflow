package daemon_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"git.sr.ht/~klahr/hazyflow/auth"
	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/daemon"
	"git.sr.ht/~klahr/hazyflow/engine"
	"git.sr.ht/~klahr/hazyflow/engine/jobstore"
	"git.sr.ht/~klahr/hazyflow/workspace"
)

// recorder collects the (subject, to) a body node saw after ${item.…}
// substitution, so the test can assert each row produced the right values.
type recorder struct {
	mu   sync.Mutex
	seen []string
}

func (r *recorder) add(s string) {
	r.mu.Lock()
	r.seen = append(r.seen, s)
	r.mu.Unlock()
}

func (r *recorder) sorted() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := append([]string(nil), r.seen...)
	sort.Strings(out)
	return out
}

// loopE2EHarness wires the REAL for_each drop (so body-mode runs through the
// worker's bodyRunner injection) plus a source emitting rows and a body
// fixture that records its post-substitution params.
func newLoopE2EHarness(t *testing.T, rec *recorder) *loopHarness {
	t.Helper()
	reg := engine.NewRegistry()

	// Real for_each + everything else from the default registry (so the
	// for_each the worker runs is the actual drop, with body-mode logic).
	for id, mf := range engine.Default.Manifests() {
		mf := mf
		nativeT, _ := engine.Default.Get(id)
		nt := nativeT
		_ = reg.Register(engine.NativeDrop{
			Manifest: mf,
			Execute: func(ctx context.Context, j core.Job, p chan<- core.Progress) (core.Result, error) {
				return nt.Execute(ctx, j, p)
			},
		})
	}

	// rows — emits a list of {name,email} maps on "out".
	_ = reg.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID: "rows", Version: "1.0", Summary: "rows",
			Examples:       []core.ParamsExample{{Title: "default"}},
			ExecutionModel: core.ExecutionBatch, ProcessModel: core.ProcessLongLived,
			Outputs: []core.Port{{Port: "out"}},
		},
		Execute: func(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
			return core.Result{JobID: job.ID, Status: core.StatusOK,
				Output: map[string]core.Ref{"out": {Inline: []any{
					map[string]any{"name": "Ada", "email": "ada@x.test"},
					map[string]any{"name": "Bo", "email": "bo@x.test"},
				}}}}, nil
		},
	})

	// sendfx — a body node. Records the "line" param it received (which the
	// graph sets to "Hi ${item.name} <${item.email}>"), proving per-item
	// substitution reached a body node's params via the engine.
	_ = reg.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID: "sendfx", Version: "1.0", Summary: "send fixture",
			Examples:       []core.ParamsExample{{Title: "default"}},
			ExecutionModel: core.ExecutionBatch, ProcessModel: core.ProcessLongLived,
			Inputs:         []core.Port{{Port: "in"}},
			Outputs:        []core.Port{{Port: "meta"}},
			ParamsSchema:   []byte(`{"type":"object","properties":{"line":{"type":"string"}}}`),
		},
		Execute: func(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
			line, _ := job.Params["line"].(string)
			rec.add(line)
			return core.Result{JobID: job.ID, Status: core.StatusOK,
				Output: map[string]core.Ref{"meta": {Inline: map[string]any{"sent": line}}}}, nil
		},
	})

	// rows_fail — like rows, but each row carries a "fail" flag the body
	// fixture keys on, so a test can make exactly one row fail.
	_ = reg.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID: "rows_fail", Version: "1.0", Summary: "rows with fail flag",
			Examples:       []core.ParamsExample{{Title: "default"}},
			ExecutionModel: core.ExecutionBatch, ProcessModel: core.ProcessLongLived,
			Outputs: []core.Port{{Port: "out"}},
		},
		Execute: func(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
			return core.Result{JobID: job.ID, Status: core.StatusOK,
				Output: map[string]core.Ref{"out": {Inline: []any{
					map[string]any{"name": "Ada", "fail": "no"},
					map[string]any{"name": "Bo", "fail": "yes"},
					map[string]any{"name": "Cy", "fail": "no"},
				}}}}, nil
		},
	})

	// mayfail — a body node that errors when its "f" param (set from
	// ${item.fail}) is "yes". Records every "name" it saw regardless.
	_ = reg.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID: "mayfail", Version: "1.0", Summary: "conditionally-failing body",
			Examples:       []core.ParamsExample{{Title: "default"}},
			ExecutionModel: core.ExecutionBatch, ProcessModel: core.ProcessLongLived,
			Inputs:         []core.Port{{Port: "in"}},
			Outputs:        []core.Port{{Port: "meta"}},
			ParamsSchema: []byte(
				`{"type":"object","properties":{"name":{"type":"string"},"f":{"type":"string"}}}`),
		},
		Execute: func(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
			name, _ := job.Params["name"].(string)
			rec.add(name)
			if f, _ := job.Params["f"].(string); f == "yes" {
				return core.Result{JobID: job.ID, Status: core.StatusError,
					Error: &core.JobError{Code: "boom", Message: "row asked to fail"}}, nil
			}
			return core.Result{JobID: job.ID, Status: core.StatusOK,
				Output: map[string]core.Ref{"meta": {Inline: map[string]any{"ok": name}}}}, nil
		},
	})

	eng := &engine.Engine{Resolver: &engine.NodeResolver{Native: reg}}
	ks := auth.NewMemKeyStore()
	role := core.Role{Name: "editor", Permissions: []core.Permission{
		core.PermGraphRun, core.PermGraphEdit, core.PermGraphAdmin,
	}}
	_, _, _ = auth.IssueAPIKey(ks, t.Context(), "k", "t", "ws", "u", []core.Role{role}, nil)
	p := core.Principal{Subject: "u", Tenant: "t", Workspace: "ws", Roles: []core.Role{role}}

	ws, _ := workspace.OpenFS("")
	jobs := jobstore.NewMemory()
	bus := daemon.NewMemoryBus()
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
		ID: "w", PollInterval: 5 * time.Millisecond, MaxRetries: 1,
		RetryBackoff: func(int) time.Duration { return time.Millisecond },
	}, jobs, eng, bus)
	go func() { _ = w.Run(wctx) }()

	return &loopHarness{svc: svc, jobs: jobs, bus: bus, principal: p}
}

// A body node inherits the PARENT run's scratch space, so file-writing drops
// (e.g. sheets_export_pdf) work inside a loop. The fixture writes a per-row
// file into job.ScratchRoot and fails the row if scratch is absent.
func TestLoopBody_BodyNodesGetParentScratch(t *testing.T) {
	rec := &recorder{}
	reg := engine.NewRegistry()

	_ = reg.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID: "rows", Version: "1.0", Summary: "rows",
			Examples:       []core.ParamsExample{{Title: "default"}},
			ExecutionModel: core.ExecutionBatch, ProcessModel: core.ProcessLongLived,
			Outputs: []core.Port{{Port: "out"}},
		},
		Execute: func(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
			return core.Result{JobID: job.ID, Status: core.StatusOK,
				Output: map[string]core.Ref{"out": {Inline: []any{
					map[string]any{"name": "Ada"},
					map[string]any{"name": "Bo"},
				}}}}, nil
		},
	})
	// scratchfx — writes name.txt into the run's scratch and reads it back.
	_ = reg.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID: "scratchfx", Version: "1.0", Summary: "scratch fixture",
			Examples:       []core.ParamsExample{{Title: "default"}},
			ExecutionModel: core.ExecutionBatch, ProcessModel: core.ProcessLongLived,
			Inputs:         []core.Port{{Port: "in"}},
			Outputs:        []core.Port{{Port: "meta"}},
			ParamsSchema:   []byte(`{"type":"object","properties":{"name":{"type":"string"}}}`),
		},
		Execute: func(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
			if job.ScratchRoot == "" {
				return core.Result{JobID: job.ID, Status: core.StatusError,
					Error: &core.JobError{Code: "no_scratch", Message: "body node has no scratch root"}}, nil
			}
			name, _ := job.Params["name"].(string)
			p := filepath.Join(job.ScratchRoot, name+".txt")
			if err := os.WriteFile(p, []byte(name), 0o644); err != nil {
				return core.Result{JobID: job.ID, Status: core.StatusError,
					Error: &core.JobError{Code: "write", Message: err.Error()}}, nil
			}
			back, err := os.ReadFile(p)
			if err != nil || string(back) != name {
				return core.Result{JobID: job.ID, Status: core.StatusError,
					Error: &core.JobError{Code: "read", Message: fmt.Sprintf("%v %q", err, back)}}, nil
			}
			rec.add(name)
			return core.Result{JobID: job.ID, Status: core.StatusOK}, nil
		},
	})

	for _, id := range []string{"for_each"} {
		mf, _ := engine.Default.Manifests()[id]
		nt, _ := engine.Default.Get(id)
		_ = reg.Register(engine.NativeDrop{Manifest: mf,
			Execute: func(ctx context.Context, j core.Job, p chan<- core.Progress) (core.Result, error) {
				return nt.Execute(ctx, j, p)
			}})
	}

	sb, err := daemon.NewFSSandbox(t.TempDir())
	if err != nil {
		t.Fatalf("sandbox: %v", err)
	}
	eng := &engine.Engine{Resolver: &engine.NodeResolver{Native: reg}, Sandbox: sb}
	ks := auth.NewMemKeyStore()
	role := core.Role{Name: "editor", Permissions: []core.Permission{
		core.PermGraphRun, core.PermGraphEdit, core.PermGraphAdmin,
	}}
	_, _, _ = auth.IssueAPIKey(ks, t.Context(), "k", "t", "ws", "u", []core.Role{role}, nil)
	p := core.Principal{Subject: "u", Tenant: "t", Workspace: "ws", Roles: []core.Role{role}}
	ws, _ := workspace.OpenFS("")
	jobs := jobstore.NewMemory()
	bus := daemon.NewMemoryBus()
	svc := &daemon.Service{
		Auth:       auth.Chain{&auth.APIKeyAuthenticator{Store: ks}},
		Workspaces: daemon.MapWorkspaces{"t/ws": ws},
		Jobs:       jobs, Engine: eng, Bus: bus,
	}
	wctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	w := daemon.NewWorker(daemon.WorkerConfig{
		ID: "w", PollInterval: 5 * time.Millisecond, MaxRetries: 1,
		RetryBackoff: func(int) time.Duration { return time.Millisecond },
	}, jobs, eng, bus)
	go func() { _ = w.Run(wctx) }()

	g := core.Graph{
		ID: "loop-scratch", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "rows", Module: "rows"},
			{ID: "loop", Module: "for_each"},
			{ID: "write", Module: "scratchfx", Params: map[string]any{"name": "${item.name}"}},
		},
		Edges: []core.Edge{
			{From: "rows", FromPort: "out", To: "loop", ToPort: "items"},
			{From: "loop", FromPort: "body", To: "write", ToPort: "in"},
		},
	}
	graphRunID, err := svc.SubmitGraph(t.Context(), p, g)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	terminal := waitForTerminalEvent(t, bus, jobs, graphRunID, 5*time.Second)
	if terminal.Status != core.JobStatusSucceeded {
		t.Fatalf("graph status = %q, want succeeded (err=%+v)", terminal.Status, terminal.Error)
	}
	if got := rec.sorted(); len(got) != 2 || got[0] != "Ada" || got[1] != "Bo" {
		t.Fatalf("scratch writes per row = %v, want [Ada Bo]", got)
	}
}

// End-to-end: a wired for_each body runs the body node once per row, with
// ${item.…} resolved per row, and the graph completes with results.
func TestLoopBody_RunsBodyPerItem(t *testing.T) {
	rec := &recorder{}
	h := newLoopE2EHarness(t, rec)

	g := core.Graph{
		ID: "loop-e2e", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "rows", Module: "rows"},
			{ID: "loop", Module: "for_each"},
			{ID: "send", Module: "sendfx", Params: map[string]any{
				"line": "Hi ${item.name} <${item.email}>",
			}},
		},
		Edges: []core.Edge{
			{From: "rows", FromPort: "out", To: "loop", ToPort: "items"},
			{From: "loop", FromPort: "body", To: "send", ToPort: "in"},
		},
	}

	graphRunID, err := h.svc.SubmitGraph(t.Context(), h.principal, g)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	terminal := waitForTerminalEvent(t, h.bus, h.jobs, graphRunID, 5*time.Second)
	if terminal.Status != core.JobStatusSucceeded {
		t.Fatalf("graph status = %q, want succeeded (err=%+v)", terminal.Status, terminal.Error)
	}

	got := rec.sorted()
	want := []string{"Hi Ada <ada@x.test>", "Hi Bo <bo@x.test>"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("body params per item = %v, want %v", got, want)
	}

	// The body node ran inside the loop — it has no standalone parent record.
	if _, err := h.jobs.Get(t.Context(), daemon.NodeJobID(graphRunID, "send")); err == nil {
		t.Error("body node 'send' should have no parent-run record (loop-owned)")
	}

	// for_each succeeded with a results entry per row.
	loopRec, err := h.jobs.Get(t.Context(), daemon.NodeJobID(graphRunID, "loop"))
	if err != nil || loopRec.Status != core.JobStatusSucceeded {
		t.Fatalf("loop status = %q (err=%v), want succeeded", loopRec.Status, err)
	}
	if loopRec.Result == nil {
		t.Fatal("loop has no result")
	}
	resultsRef, ok := loopRec.Result.Output["results"]
	if !ok {
		t.Fatal("loop result missing 'results' port")
	}
	if list, ok := resultsRef.Inline.([]core.Ref); !ok || len(list) != 2 {
		t.Fatalf("results = %#v, want 2 entries", resultsRef.Inline)
	}
}

// fail_fast OFF: one failing row doesn't sink the loop — the other rows still
// run, the loop succeeds, and the failure surfaces (keyed by index) on the
// errors port.
func TestLoopBody_PerItemErrorIsolation(t *testing.T) {
	rec := &recorder{}
	h := newLoopE2EHarness(t, rec)

	g := core.Graph{
		ID: "loop-iso", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "rows", Module: "rows_fail"},
			{ID: "loop", Module: "for_each"},
			{ID: "step", Module: "mayfail", Params: map[string]any{
				"name": "${item.name}",
				"f":    "${item.fail}",
			}},
		},
		Edges: []core.Edge{
			{From: "rows", FromPort: "out", To: "loop", ToPort: "items"},
			{From: "loop", FromPort: "body", To: "step", ToPort: "in"},
		},
	}

	graphRunID, err := h.svc.SubmitGraph(t.Context(), h.principal, g)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	terminal := waitForTerminalEvent(t, h.bus, h.jobs, graphRunID, 5*time.Second)
	if terminal.Status != core.JobStatusSucceeded {
		t.Fatalf("graph status = %q, want succeeded (failures isolated)", terminal.Status)
	}

	// All three rows ran (the failing one didn't stop the others).
	if got := rec.sorted(); len(got) != 3 {
		t.Errorf("rows run = %v, want 3", got)
	}

	loopRec, _ := h.jobs.Get(t.Context(), daemon.NodeJobID(graphRunID, "loop"))
	if loopRec.Status != core.JobStatusSucceeded {
		t.Fatalf("loop status = %q, want succeeded", loopRec.Status)
	}
	errsRef := loopRec.Result.Output["errors"]
	errsList, ok := errsRef.Inline.([]any)
	if !ok {
		t.Fatalf("errors = %#v, want list of failed rows", errsRef.Inline)
	}
	// Exactly the one failing row (Bo, 1-based row 2) is recorded, carrying
	// the row's own data so the entry is self-describing.
	if len(errsList) != 1 {
		t.Fatalf("errors = %v, want exactly 1 failed row", errsList)
	}
	entry, _ := errsList[0].(map[string]any)
	if entry["row"] != 2 {
		t.Errorf("row = %v, want 2 (Bo is the second row)", entry["row"])
	}
	data, _ := entry["data"].(map[string]any)
	if data["name"] != "Bo" {
		t.Errorf("data = %v, want the failing row's fields (name=Bo)", data)
	}
}

// fail_fast ON: a failing row fails the loop, which propagates to the graph.
func TestLoopBody_FailFastFailsGraph(t *testing.T) {
	rec := &recorder{}
	h := newLoopE2EHarness(t, rec)

	g := core.Graph{
		ID: "loop-ff", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "rows", Module: "rows_fail"},
			{ID: "loop", Module: "for_each", Params: map[string]any{"fail_fast": true}},
			{ID: "step", Module: "mayfail", Params: map[string]any{
				"name": "${item.name}",
				"f":    "${item.fail}",
			}},
		},
		Edges: []core.Edge{
			{From: "rows", FromPort: "out", To: "loop", ToPort: "items"},
			{From: "loop", FromPort: "body", To: "step", ToPort: "in"},
		},
	}

	graphRunID, err := h.svc.SubmitGraph(t.Context(), h.principal, g)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	terminal := waitForTerminalEvent(t, h.bus, h.jobs, graphRunID, 5*time.Second)
	if terminal.Status != core.JobStatusFailed {
		t.Fatalf("graph status = %q, want failed (fail_fast)", terminal.Status)
	}
	loopRec, _ := h.jobs.Get(t.Context(), daemon.NodeJobID(graphRunID, "loop"))
	if loopRec.Status != core.JobStatusFailed {
		t.Errorf("loop status = %q, want failed", loopRec.Status)
	}
}
