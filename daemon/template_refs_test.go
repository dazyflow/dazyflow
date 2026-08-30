// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/engine/jobstore"
)

// webhook → if → email is the shape that broke. The email step's direct
// predecessor is the `if`, so before this the trigger was simply not there:
// ${upstream.webhook_input_1.body} failed the node, and ${trigger.body.version}
// was left in the text and mailed out.

func chainGraph(emailParams map[string]any) core.Graph {
	return core.Graph{
		ID: "release", Tenant: "acme", Workspace: "main",
		Nodes: []core.Node{
			{ID: "webhook_input_1", Module: "webhook_input"},
			{ID: "if_1", Module: "if"},
			{ID: "email_send_1", Module: "email_send", Params: emailParams},
		},
		Edges: []core.Edge{
			{From: "webhook_input_1", FromPort: "body", To: "if_1", ToPort: "a"},
			{From: "if_1", FromPort: "then", To: "email_send_1", ToPort: "pass"},
		},
	}
}

func TestTemplateRefs(t *testing.T) {
	cases := []struct {
		name        string
		params      map[string]any
		wantNodes   []string
		wantTrigger bool
	}{
		{"none", map[string]any{"body": "hello"}, nil, false},
		{"upstream", map[string]any{"body": "${upstream.webhook_input_1.body}"}, []string{"webhook_input_1"}, false},
		{"trigger", map[string]any{"body": "${trigger.body.version}"}, nil, true},
		{"the whole trigger port, no path", map[string]any{"body": "${trigger.body}"}, nil, true},
		{"both, and deduplicated", map[string]any{
			"a": "${upstream.n1.out} ${upstream.n1.other}",
			"b": "${trigger.body.x}",
		}, []string{"n1"}, true},
		{
			// Templates live at any depth in a params tree — a header map, a
			// list of recipients — so this cannot look at top-level strings.
			"nested inside maps and slices",
			map[string]any{"headers": map[string]any{"X-V": "${upstream.n2.out}"},
				"to": []any{"a@b.c", "${upstream.n3.email}"}},
			[]string{"n2", "n3"}, false,
		},
		{
			// A secret reference is a different scheme with its own resolver;
			// prefetching a node called "vault" would be nonsense.
			"leaves other schemes alone",
			map[string]any{"key": "${secret.api_key}", "x": "${item.name}"},
			nil, false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			nodes, trigger := templateRefs(chainGraph(c.params), "email_send_1")
			if trigger != c.wantTrigger {
				t.Errorf("wantsTrigger = %v, want %v", trigger, c.wantTrigger)
			}
			if len(nodes) != len(c.wantNodes) {
				t.Fatalf("nodes = %v, want %v", nodes, c.wantNodes)
			}
			got := map[string]bool{}
			for _, n := range nodes {
				got[n] = true
			}
			for _, want := range c.wantNodes {
				if !got[want] {
					t.Errorf("nodes = %v, missing %q", nodes, want)
				}
			}
		})
	}
}

// seedRun writes the completed records a real run would leave behind: the
// trigger's seeded result and the `if` the email step actually hangs off.
func seedRun(t *testing.T, w *Worker, runID string) {
	t.Helper()
	ctx := context.Background()
	for _, n := range []struct {
		id  string
		res core.Result
	}{
		{"webhook_input_1", core.Result{Status: core.StatusOK, Output: map[string]core.Ref{
			"body": {Inline: map[string]any{"version": "0.27.5"}},
		}}},
		{"if_1", core.Result{Status: core.StatusOK, Output: map[string]core.Ref{
			"then": {Inline: "yes"},
		}}},
	} {
		id := NodeJobID(runID, n.id)
		if err := w.store.Enqueue(ctx, core.JobRecord{
			ID: id, Kind: core.JobKindNode, GraphRunID: runID, GraphID: "release",
			NodeID: n.id, Tenant: "acme", Workspace: "main",
		}); err != nil {
			t.Fatalf("enqueue %s: %v", n.id, err)
		}
		res := n.res
		res.JobID = id
		if err := w.store.Complete(ctx, id, core.JobStatusSucceeded, &res); err != nil {
			t.Fatalf("complete %s: %v", n.id, err)
		}
	}
}

func chainWorker(t *testing.T) *Worker {
	t.Helper()
	return &Worker{store: jobstore.NewMemory()}
}

func TestAddTemplateResults_ReachesTheTriggerTwoHopsUp(t *testing.T) {
	w := chainWorker(t)
	seedRun(t, w, "run-1")
	g := chainGraph(map[string]any{"body": "Version ${trigger.body.version} shipped"})
	rec := core.JobRecord{GraphRunID: "run-1", NodeID: "email_send_1"}

	prior := map[string]core.Result{"if_1": {Status: core.StatusOK}} // the direct predecessor
	w.addTemplateResults(context.Background(), g, rec, prior)

	if _, ok := prior["webhook_input_1"]; !ok {
		t.Fatalf("the trigger is not reachable: %v", priorKeys(prior))
	}
}

func TestAddTemplateResults_ReachesANamedUpstreamNode(t *testing.T) {
	w := chainWorker(t)
	seedRun(t, w, "run-1")
	g := chainGraph(map[string]any{"body": "${upstream.webhook_input_1.body}"})
	rec := core.JobRecord{GraphRunID: "run-1", NodeID: "email_send_1"}

	prior := map[string]core.Result{"if_1": {Status: core.StatusOK}}
	w.addTemplateResults(context.Background(), g, rec, prior)

	if _, ok := prior["webhook_input_1"]; !ok {
		t.Fatalf("the named node is not reachable: %v", priorKeys(prior))
	}
}

func TestAddTemplateResults_FetchesNothingWhenNothingIsReferenced(t *testing.T) {
	// The common case. A flow must not turn into a scan of its own run just by
	// having nodes in it.
	w := chainWorker(t)
	seedRun(t, w, "run-1")
	g := chainGraph(map[string]any{"body": "a plain message"})
	rec := core.JobRecord{GraphRunID: "run-1", NodeID: "email_send_1"}

	prior := map[string]core.Result{"if_1": {Status: core.StatusOK}}
	w.addTemplateResults(context.Background(), g, rec, prior)

	if len(prior) != 1 {
		t.Errorf("prefetched %v for a node that references nothing", priorKeys(prior))
	}
}

func TestAddTemplateResults_NeverOverwritesARealPredecessor(t *testing.T) {
	// The direct predecessor's result is already loaded and is the authority.
	w := chainWorker(t)
	seedRun(t, w, "run-1")
	g := chainGraph(map[string]any{"body": "${upstream.if_1.then}"})
	rec := core.JobRecord{GraphRunID: "run-1", NodeID: "email_send_1"}

	mine := core.Result{Status: core.StatusOK, Output: map[string]core.Ref{"then": {Inline: "loaded-by-edge"}}}
	prior := map[string]core.Result{"if_1": mine}
	w.addTemplateResults(context.Background(), g, rec, prior)

	if got := prior["if_1"].Output["then"].Inline; got != "loaded-by-edge" {
		t.Errorf("overwrote the predecessor's result with %v", got)
	}
}

func TestAddTemplateResults_LeavesAMissingNodeAbsent(t *testing.T) {
	// So the substituter reports it. Inventing an empty result here would put
	// the silence back in a new place.
	w := chainWorker(t)
	seedRun(t, w, "run-1")
	g := chainGraph(map[string]any{"body": "${upstream.never_ran.out}"})
	rec := core.JobRecord{GraphRunID: "run-1", NodeID: "email_send_1"}

	prior := map[string]core.Result{}
	w.addTemplateResults(context.Background(), g, rec, prior)

	if _, ok := prior["never_ran"]; ok {
		t.Error("invented a result for a node that never ran")
	}
}

func priorKeys(m map[string]core.Result) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
