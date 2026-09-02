// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package chaos

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/daemon"
	hfnet "github.com/dazyflow/dazyflow/drops/net"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/engine/jobstore"
	"github.com/dazyflow/dazyflow/workspace"
)

// A published webhook flow whose last step calls its own trigger URL used
// to run forever: every iteration is a fresh TOP-LEVEL run, so the subgraph
// depth cap and the per-tree fan-out budget — which walk parent links inside
// one run tree — never saw it. Measured at ~5 runs/second, climbing for as
// long as the daemon was up.
//
// A step calling one of OUR OWN urls now carries the trigger-chain depth
// (core.TriggerDepthHeader) and the endpoint refuses past
// core.MaxTriggerChainDepth, so the chain dies out.
func TestTriggerLoop_IsBroken(t *testing.T) {
	hfnet.SetAllowPrivateEgress(true)
	t.Cleanup(func() { hfnet.SetAllowPrivateEgress(false) })

	ks := auth.NewMemKeyStore()
	wsStore, _ := workspace.OpenFS("")
	jobs := jobstore.NewMemory()
	bus := daemon.NewMemoryBus()
	eng := &engine.Engine{Resolver: &engine.NodeResolver{Native: engine.Default}}
	svc := &daemon.Service{
		Auth:       auth.Chain{&auth.APIKeyAuthenticator{Store: ks}},
		Workspaces: daemon.MapWorkspaces{"acme/ws1": wsStore},
		Jobs:       jobs,
		Engine:     eng,
		Bus:        bus,
	}
	wctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	w := daemon.NewWorker(daemon.WorkerConfig{ID: "w", PollInterval: 5 * time.Millisecond, MaxRetries: 1}, jobs, eng, bus)
	go func() { _ = w.Run(wctx) }()

	wh := daemon.NewWebhookListener(svc)
	mux := http.NewServeMux()
	mux.HandleFunc("/trigger/", func(rw http.ResponseWriter, r *http.Request) {
		daemon.ServeWebhookForTest(wh, rw, r)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	selfURL := ts.URL + "/trigger/acme/ws1/selfloop"

	// The daemon has to know which origin is itself — dzd does this from
	// DAZYFLOW_PUBLIC_BASE_URL.
	hfnet.SetSelfOrigin(ts.URL)
	t.Cleanup(func() { hfnet.SetSelfOrigin("") })

	g := core.Graph{
		ID: "selfloop", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{
			{ID: "inbound", Module: "webhook_input", Params: map[string]any{"secrets": []any{"s"}}},
			{ID: "again", Module: "http_request", Params: map[string]any{
				"url": selfURL, "method": "POST", "body": `{"go":1}`,
				"allow_private_networks": true,
				"headers": map[string]any{
					"Authorization": "Bearer s", "Content-Type": "application/json",
				},
			}},
		},
		Edges: []core.Edge{{From: "inbound", FromPort: "body", To: "again", ToPort: "request_body"}},
	}
	commit, err := wsStore.Save(g, "qa")
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := wsStore.PromoteToEnvironment(g.ID, workspace.PublishedEnv, commit); err != nil {
		t.Fatalf("publish: %v", err)
	}

	req, _ := http.NewRequest("POST", selfURL, bytes.NewReader([]byte(`{"go":1}`)))
	req.Header.Set("Authorization", "Bearer s")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("kick off: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("kickoff status=%d", resp.StatusCode)
	}

	var counts []int
	for i := 0; i < 5; i++ {
		time.Sleep(time.Second)
		recs, _ := jobs.ListGraphRuns(context.Background(), core.ListGraphRunsOpts{Limit: 1000000})
		counts = append(counts, len(recs))
	}
	t.Logf("graph runs after 1..5s: %v", counts)
	if counts[len(counts)-1] > counts[0] {
		t.Errorf("one deliberate trigger is still spawning runs after 5s (%v) — the chain never breaks", counts)
	}
	// One kick-off plus the chain it is allowed before the cap bites.
	if last := counts[len(counts)-1]; last > core.MaxTriggerChainDepth+1 {
		t.Errorf("chain ran to %d runs, want at most %d", last, core.MaxTriggerChainDepth+1)
	}
}

// FINDING: `delay` computes time.Duration(ms) * time.Millisecond without an
// overflow check, so any ms above ~9.2e12 (≈292 years) wraps and the step
// reports SUCCESS immediately — "wait" silently becomes "don't wait". Below
// that threshold there is no upper bound either: only the worker's 30-minute
// DefaultNodeTimeout ends it, and DAZYFLOW_MAX_GRAPH_TIMEOUT defaults to 0,
// so no run-level ceiling applies.
func TestDelay_RejectsAbsurdDurations(t *testing.T) {
	for _, ms := range []int64{1 << 62, 9223372036854775} {
		t.Run(fmt.Sprint(ms), func(t *testing.T) {
			hs := newHarness(t)
			start := time.Now()
			status, err := hs.submit(graph("overflow",
				[]core.Node{{ID: "wait", Module: "delay", Params: map[string]any{"ms": ms}}}, nil), 20*time.Second)
			t.Logf("ms=%d status=%q err=%v elapsed=%s", ms, status, err, time.Since(start).Round(time.Millisecond))
			if status == core.JobStatusSucceeded {
				t.Errorf("delay of %d ms reported success after %s — duration overflow, the wait never happened",
					ms, time.Since(start).Round(time.Millisecond))
			}
		})
	}
}

// The recursion guards hold — regression cover for them.
func TestSubgraphRecursion_IsBounded(t *testing.T) {
	t.Run("flow calls itself", func(t *testing.T) {
		hs := newHarness(t)
		g := graph("recurse",
			[]core.Node{
				{ID: "start", Module: "delay", Params: map[string]any{"ms": 0}},
				{ID: "call", Module: "subgraph", Params: map[string]any{"graph_id": "recurse"}},
			},
			[]core.Edge{{From: "start", FromPort: "pass", To: "call", ToPort: "in"}})
		hs.save(g)
		status, err := hs.submit(g, 60*time.Second)
		t.Logf("status=%q err=%v runs=%d", status, err, hs.countRuns())
		if status != core.JobStatusFailed {
			t.Errorf("self-recursive flow ended %q, want failed at the depth cap", status)
		}
		if runs := hs.countRuns(); runs > 16 {
			t.Errorf("depth cap let %d runs through", runs)
		}
	})

	t.Run("two flows call each other", func(t *testing.T) {
		hs := newHarness(t)
		mk := func(id, other string) core.Graph {
			return graph(id,
				[]core.Node{
					{ID: "start", Module: "delay", Params: map[string]any{"ms": 0}},
					{ID: "call", Module: "subgraph", Params: map[string]any{"graph_id": other}},
				},
				[]core.Edge{{From: "start", FromPort: "pass", To: "call", ToPort: "in"}})
		}
		a := mk("ping", "pong")
		hs.save(a)
		hs.save(mk("pong", "ping"))
		status, _ := hs.submit(a, 60*time.Second)
		if status != core.JobStatusFailed {
			t.Errorf("mutual recursion ended %q, want failed", status)
		}
	})

	t.Run("wide self-recursive fan-out", func(t *testing.T) {
		hs := newHarness(t)
		const width = 8
		nodes := []core.Node{{ID: "start", Module: "delay", Params: map[string]any{"ms": 0}}}
		var edges []core.Edge
		for i := 0; i < width; i++ {
			id := fmt.Sprintf("call%d", i)
			nodes = append(nodes, core.Node{ID: id, Module: "subgraph", Params: map[string]any{"graph_id": "bomb"}})
			edges = append(edges, core.Edge{From: "start", FromPort: "pass", To: id, ToPort: "in"})
		}
		g := graph("bomb", nodes, edges)
		hs.save(g)
		status, _ := hs.submit(g, 3*time.Minute)
		runs := hs.countRuns()
		t.Logf("status=%q runs=%d", status, runs)
		if runs > 2000 {
			t.Errorf("fan-out budget let %d descendant runs through", runs)
		}
	})
}

// Loop-body misuse the editor allows: each must terminate, not hang.
func TestLoopBodyMisuse_Terminates(t *testing.T) {
	src := func() ([]core.Node, []core.Edge) {
		return []core.Node{
				{ID: "src", Module: "text", Params: map[string]any{"text": `["a","b","c"]`}},
				{ID: "parse", Module: "parse_json"},
			},
			[]core.Edge{{From: "src", FromPort: "out", To: "parse", ToPort: "in"}}
	}
	cases := map[string]func() core.Graph{
		"body wired back into the main flow": func() core.Graph {
			n, e := src()
			n = append(n,
				core.Node{ID: "loop", Module: "for_each"},
				core.Node{ID: "work", Module: "delay", Params: map[string]any{"ms": 0}},
				core.Node{ID: "after", Module: "delay", Params: map[string]any{"ms": 0}})
			e = append(e,
				core.Edge{From: "parse", FromPort: "out", To: "loop", ToPort: "items"},
				core.Edge{From: "loop", FromPort: "body", To: "work", ToPort: "pass"},
				core.Edge{From: "work", FromPort: "pass", To: "after", ToPort: "pass"},
				core.Edge{From: "parse", FromPort: "out", To: "after", ToPort: "pass"})
			return graph("loopback", n, e)
		},
		"two loops share one body step": func() core.Graph {
			n, e := src()
			n = append(n,
				core.Node{ID: "loop1", Module: "for_each"},
				core.Node{ID: "loop2", Module: "for_each"},
				core.Node{ID: "shared", Module: "delay", Params: map[string]any{"ms": 0}})
			e = append(e,
				core.Edge{From: "parse", FromPort: "out", To: "loop1", ToPort: "items"},
				core.Edge{From: "parse", FromPort: "out", To: "loop2", ToPort: "items"},
				core.Edge{From: "loop1", FromPort: "body", To: "shared", ToPort: "pass"},
				core.Edge{From: "loop2", FromPort: "body", To: "shared", ToPort: "pass"})
			return graph("sharedbody", n, e)
		},
		"a parking step inside a loop body": func() core.Graph {
			n, e := src()
			n = append(n,
				core.Node{ID: "loop", Module: "for_each"},
				core.Node{ID: "gate", Module: "await_approval", Params: map[string]any{"approvers": []any{"qa@acme"}}})
			e = append(e,
				core.Edge{From: "parse", FromPort: "out", To: "loop", ToPort: "items"},
				core.Edge{From: "loop", FromPort: "body", To: "gate", ToPort: "pass"})
			return graph("loopapproval", n, e)
		},
		"a child-graph step inside a loop body": func() core.Graph {
			n, e := src()
			n = append(n,
				core.Node{ID: "loop", Module: "for_each"},
				core.Node{ID: "call", Module: "subgraph", Params: map[string]any{"graph_id": "kid"}})
			e = append(e,
				core.Edge{From: "parse", FromPort: "out", To: "loop", ToPort: "items"},
				core.Edge{From: "loop", FromPort: "body", To: "call", ToPort: "in"})
			return graph("loopsub", n, e)
		},
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			hs := newHarness(t)
			hs.save(graph("kid", []core.Node{{ID: "n", Module: "delay", Params: map[string]any{"ms": 0}}}, nil))
			g := mk()
			status, err := hs.submit(g, 30*time.Second)
			t.Logf("status=%q err=%v", status, err)
			if status == statusHung {
				t.Errorf("run never reached a terminal status")
			}
		})
	}
}

// Parked approvals must not hold worker slots.
func TestParkedApprovals_DoNotStarveWorkers(t *testing.T) {
	hs := newHarness(t)
	for i := 0; i < 20; i++ {
		g := graph(fmt.Sprintf("park%d", i),
			[]core.Node{{ID: "gate", Module: "await_approval", Params: map[string]any{"approvers": []any{"qa@acme"}}}}, nil)
		if _, err := hs.svc.SubmitGraph(t.Context(), hs.p, g); err != nil {
			t.Fatalf("submit park%d: %v", i, err)
		}
	}
	status, err := hs.submit(graph("after",
		[]core.Node{{ID: "x", Module: "delay", Params: map[string]any{"ms": 0}}}, nil), 30*time.Second)
	if status != core.JobStatusSucceeded {
		t.Errorf("plain flow behind 20 parked approvals: status=%q err=%v", status, err)
	}
}
