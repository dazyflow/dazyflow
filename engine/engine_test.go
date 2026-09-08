// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
)

// noopManifest describes a module with one optional input and one output,
// used by most tests to assemble arbitrary graph shapes.
var noopManifest = core.Manifest{
	ID:       "noop",
	Summary:  "Test fixture no-op.",
	Examples: []core.ParamsExample{{Title: "default"}},
	Inputs:   []core.Port{{Port: "in"}},
	Outputs:  []core.Port{{Port: "out"}},
}

func newEngineWith(t *testing.T, nodes ...NativeDrop) *Engine {
	t.Helper()
	reg := NewRegistry()
	for _, n := range nodes {
		if err := reg.Register(n); err != nil {
			t.Fatalf("register: %v", err)
		}
	}
	return &Engine{Resolver: &NodeResolver{Native: reg}}
}

func TestEngine_LinearChain(t *testing.T) {
	var captured sync.Map // nodeID -> map[string]core.Ref

	e := newEngineWith(t, NativeDrop{
		Manifest: noopManifest,
		Execute: func(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
			captured.Store(job.NodeID, job.Input)
			return core.Result{
				Status: core.StatusOK,
				Output: map[string]core.Ref{"out": {Ref: job.NodeID + ":out"}},
			}, nil
		},
	})

	g := core.Graph{
		ID: "g",
		Nodes: []core.Node{
			{ID: "a", Module: "noop"},
			{ID: "b", Module: "noop"},
			{ID: "c", Module: "noop"},
		},
		Edges: []core.Edge{
			{From: "a", FromPort: "out", To: "b", ToPort: "in"},
			{From: "b", FromPort: "out", To: "c", ToPort: "in"},
		},
	}

	res, err := e.Run(t.Context(), g, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status = %q, want ok", res.Status)
	}
	for _, id := range []string{"a", "b", "c"} {
		if _, ok := res.Nodes[id]; !ok {
			t.Errorf("missing result for %q", id)
		}
	}
	if got := loadInput(t, &captured, "b"); got["in"].Ref != "a:out" {
		t.Errorf("b.in = %+v, want ref=a:out", got["in"])
	}
	if got := loadInput(t, &captured, "c"); got["in"].Ref != "b:out" {
		t.Errorf("c.in = %+v, want ref=b:out", got["in"])
	}
}

func TestEngine_DiamondParallelism(t *testing.T) {
	var arrived int32
	barrier := make(chan struct{})

	exec := func(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
		if job.NodeID == "b" || job.NodeID == "c" {
			if atomic.AddInt32(&arrived, 1) == 2 {
				close(barrier)
			}
			select {
			case <-barrier:
			case <-ctx.Done():
				return core.Result{Status: core.StatusError}, ctx.Err()
			}
		}
		return core.Result{
			Status: core.StatusOK,
			Output: map[string]core.Ref{"out": {Ref: job.NodeID}},
		}, nil
	}

	merge := core.Manifest{
		ID:       "merge",
		Summary:  "Test fixture merge.",
		Examples: []core.ParamsExample{{Title: "default"}},
		Inputs:   []core.Port{{Port: "in", Variadic: true}},
		Outputs:  []core.Port{{Port: "out"}},
	}

	e := newEngineWith(t,
		NativeDrop{Manifest: noopManifest, Execute: exec},
		NativeDrop{Manifest: merge, Execute: exec},
	)

	g := core.Graph{
		ID: "diamond",
		Nodes: []core.Node{
			{ID: "a", Module: "noop"},
			{ID: "b", Module: "noop"},
			{ID: "c", Module: "noop"},
			{ID: "d", Module: "merge"},
		},
		Edges: []core.Edge{
			{From: "a", FromPort: "out", To: "b", ToPort: "in"},
			{From: "a", FromPort: "out", To: "c", ToPort: "in"},
			{From: "b", FromPort: "out", To: "d", ToPort: "in"},
			{From: "c", FromPort: "out", To: "d", ToPort: "in"},
		},
	}

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	res, err := e.Run(ctx, g, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status = %q, want ok", res.Status)
	}
	if atomic.LoadInt32(&arrived) != 2 {
		t.Fatalf("expected both b and c to enter barrier; arrived=%d", arrived)
	}
}

func TestEngine_NodeErrorAborts(t *testing.T) {
	var ranC atomic.Bool

	e := newEngineWith(t, NativeDrop{
		Manifest: noopManifest,
		Execute: func(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
			switch job.NodeID {
			case "b":
				return core.Result{
					Status: core.StatusError,
					Error:  &core.JobError{Code: "boom", Message: "intentional"},
				}, nil
			case "c":
				ranC.Store(true)
			}
			return core.Result{Status: core.StatusOK, Output: map[string]core.Ref{"out": {Ref: job.NodeID}}}, nil
		},
	})

	g := core.Graph{
		Nodes: []core.Node{
			{ID: "a", Module: "noop"},
			{ID: "b", Module: "noop"},
			{ID: "c", Module: "noop"},
		},
		Edges: []core.Edge{
			{From: "a", FromPort: "out", To: "b", ToPort: "in"},
			{From: "b", FromPort: "out", To: "c", ToPort: "in"},
		},
	}

	res, err := e.Run(t.Context(), g, nil)
	if err == nil {
		t.Fatal("expected error from failing node")
	}
	if res.Status != core.StatusError {
		t.Errorf("status = %q, want error", res.Status)
	}
	if ranC.Load() {
		t.Error("downstream node c should not have executed")
	}
	if res.Error == nil || !strings.Contains(res.Error.Message, "intentional") {
		t.Errorf("error = %+v, want one mentioning 'intentional'", res.Error)
	}
}

func TestEngine_ProgressForwarded(t *testing.T) {
	pct := 0.5
	e := newEngineWith(t, NativeDrop{
		Manifest: noopManifest,
		Execute: func(_ context.Context, job core.Job, progress chan<- core.Progress) (core.Result, error) {
			progress <- core.Progress{JobID: job.ID, NodeID: job.NodeID, Percent: &pct, Message: "halfway"}
			return core.Result{Status: core.StatusOK, Output: map[string]core.Ref{"out": {Ref: "x"}}}, nil
		},
	})

	progress := make(chan GraphProgress, 4)
	g := core.Graph{Nodes: []core.Node{{ID: "a", Module: "noop"}}}

	if _, err := e.Run(t.Context(), g, progress); err != nil {
		t.Fatalf("Run: %v", err)
	}

	var got []GraphProgress
	for p := range progress {
		got = append(got, p)
	}
	if len(got) != 1 {
		t.Fatalf("got %d progress events, want 1: %+v", len(got), got)
	}
	if got[0].NodeID != "a" || got[0].Progress.Message != "halfway" {
		t.Errorf("unexpected event %+v", got[0])
	}
}

func TestEngine_ContextCancel(t *testing.T) {
	started := make(chan struct{})

	e := newEngineWith(t, NativeDrop{
		Manifest: noopManifest,
		Execute: func(ctx context.Context, _ core.Job, _ chan<- core.Progress) (core.Result, error) {
			close(started)
			<-ctx.Done()
			return core.Result{Status: core.StatusError, Error: &core.JobError{Code: "cancelled"}}, ctx.Err()
		},
	})

	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		<-started
		cancel()
	}()

	g := core.Graph{Nodes: []core.Node{{ID: "a", Module: "noop"}}}
	_, err := e.Run(ctx, g, nil)
	if !errors.Is(err, context.Canceled) {
		// engine wraps node error; check via string fallback
		if err == nil || !strings.Contains(err.Error(), "context canceled") {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	}
}

func TestEngine_UnknownModuleFailsValidation(t *testing.T) {
	e := newEngineWith(t) // empty registry

	g := core.Graph{Nodes: []core.Node{{ID: "a", Module: "missing"}}}
	res, err := e.Run(t.Context(), g, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "unknown module") {
		t.Errorf("error %q does not mention 'unknown module'", err.Error())
	}
	if res.Status != core.StatusError {
		t.Errorf("status = %q, want error", res.Status)
	}
}

func loadInput(t *testing.T, m *sync.Map, id string) map[string]core.Ref {
	t.Helper()
	v, ok := m.Load(id)
	if !ok {
		t.Fatalf("no captured input for %q", id)
	}
	return v.(map[string]core.Ref)
}

// cloneNodeIO must hand back a DEEP copy. Secret resolution writes the
// resolved cleartext into the params it is given, and the graph those
// params come from is shared by every step of the run and — via the
// worker's run cache — by every concurrent run of the same flow. A
// shallow clone leaves nested maps aliased, so one run's resolved secret
// values land in another's graph.
func TestCloneNodeIO_DeepCopiesNestedParams(t *testing.T) {
	params := map[string]any{
		"top":    "a",
		"nested": map[string]any{"secret": "${secret.token}"},
		"list":   []any{map[string]any{"k": "v"}},
	}
	env := map[string]string{"E": "1"}

	gotParams, gotEnv := cloneNodeIO(params, env)

	gotParams["nested"].(map[string]any)["secret"] = "resolved-cleartext"
	if orig := params["nested"].(map[string]any)["secret"]; orig != "${secret.token}" {
		t.Errorf("nested param leaked into the caller's map: %q", orig)
	}

	gotParams["list"].([]any)[0].(map[string]any)["k"] = "resolved-cleartext"
	if orig := params["list"].([]any)[0].(map[string]any)["k"]; orig != "v" {
		t.Errorf("nested list element leaked into the caller's map: %q", orig)
	}

	// Env is flat, so a shallow clone is exact — but it must still be a copy.
	gotEnv["E"] = "2"
	if env["E"] != "1" {
		t.Errorf("env leaked into the caller's map: %q", env["E"])
	}
}

// A failed entropy read has to surface as an error, never as an empty id:
// an empty job id would collide across every run in the store.
func TestNewJobID_IsHexAndUnique(t *testing.T) {
	id, err := newJobID()
	if err != nil {
		t.Fatalf("newJobID: %v", err)
	}
	if len(id) != 32 {
		t.Errorf("id = %q (len %d), want 32 hex chars for 16 bytes", id, len(id))
	}
	if _, err := hex.DecodeString(id); err != nil {
		t.Errorf("id %q is not hex: %v", id, err)
	}

	other, err := newJobID()
	if err != nil {
		t.Fatalf("newJobID (second call): %v", err)
	}
	if id == other {
		t.Error("two consecutive ids are identical; the entropy read is not reaching the id")
	}
}

// Modules are not required to echo the job id back. The engine stamps it
// on any result that arrives without one, because the job store and the
// run-detail UI key on it.
func TestEngine_StampsJobIDOnResultWithoutOne(t *testing.T) {
	e := newEngineWith(t, NativeDrop{
		Manifest: noopManifest,
		Execute: func(_ context.Context, _ core.Job, _ chan<- core.Progress) (core.Result, error) {
			// Deliberately leaves JobID unset.
			return core.Result{
				Status: core.StatusOK,
				Output: map[string]core.Ref{"out": {Ref: "x"}},
			}, nil
		},
	})

	res, err := e.Run(t.Context(), core.Graph{
		ID: "g", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{{ID: "a", Module: "noop"}},
	}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := res.Nodes["a"].JobID; got == "" {
		t.Error("node result carries no JobID; the engine must stamp the job's own id")
	}
}
