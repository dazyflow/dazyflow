// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package chaos

import (
	"strings"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
)

// A fallback edge is the failure path. What happens to the dependent when the
// source SUCCEEDS — is it skipped (and the run finishes) or waited on forever?
func TestFallbackEdge_WhenSourceSucceeds(t *testing.T) {
	h := newHarness(t)
	g := graph("fbok",
		[]core.Node{textNode("a", "hello"), b64Node("b"), b64Node("c")},
		[]core.Edge{
			{From: "a", FromPort: "out", To: "b", ToPort: "in"},
			{From: "b", FromPort: "out", To: "c", ToPort: "in", OnError: core.OnErrorFallback},
		})
	st, err := h.submit(g, 30*time.Second)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if st == statusHung {
		t.Errorf("FINDING: run never reached a terminal status — a fallback edge whose source succeeded strands the dependent")
	}
	t.Logf("status=%s", st)
}

// Degenerate runs: nothing to do, or nothing enabled. Both must terminate.
func TestEmptyAndDisabledGraphs_Terminate(t *testing.T) {
	h := newHarness(t)
	disabled := func(n core.Node) core.Node { n.Disabled = true; return n }

	cases := map[string]core.Graph{
		"no nodes at all": graph("empty", nil, nil),
		"every node switched off": graph("alloff",
			[]core.Node{disabled(textNode("a", "x")), disabled(b64Node("b"))},
			[]core.Edge{{From: "a", FromPort: "out", To: "b", ToPort: "in"}}),
		"only source switched off": graph("srcoff",
			[]core.Node{disabled(textNode("a", "x")), b64Node("b")},
			[]core.Edge{{From: "a", FromPort: "out", To: "b", ToPort: "in"}}),
		"a lone breakpoint on the only node": graph("bp",
			[]core.Node{func() core.Node { n := textNode("a", "x"); n.Breakpoint = true; return n }()}, nil),
	}
	for name, g := range cases {
		t.Run(name, func(t *testing.T) {
			st, err := h.submit(g, 20*time.Second)
			if err != nil {
				t.Logf("submit refused: %v", err)
				return
			}
			if st == statusHung {
				t.Errorf("FINDING: never reached a terminal status")
			}
			t.Logf("status=%s", st)
		})
	}
}

// A flow's ID becomes a path ("graphs/<id>.json") and a git tag component
// ("graphs/<id>/published"), and used to be validated nowhere in between:
// "a/../../escape" saved a flow OUTSIDE graphs/ that could then never be
// loaded, published or deleted, and a 300-character ID worked on the in-memory
// store and failed with ENAMETOOLONG on disk.
func TestGraphID_IsValidated(t *testing.T) {
	h := newHarness(t)
	for _, id := range []string{
		"a/../../escape",
		"../.git/config",
		"..",
		".",
		"with\nnewline",
		"with space/and/slashes",
		"with space",
		strings.Repeat("n", 300),
		"",
	} {
		g := core.Graph{ID: id, Tenant: "acme", Workspace: "ws1", Nodes: []core.Node{textNode("a", "x")}}
		commit, err := h.svc.SaveGraph(t.Context(), h.p, g)
		if err != nil {
			t.Logf("id=%-24q refused: %v", id, err)
			continue
		}
		_, loadErr := h.ws.Load(id)
		t.Errorf("FINDING: id=%q stored (commit %.8s); reload: %v", id, commit, loadErr)
	}

	// What the editor produces still saves, and reloads.
	g := core.Graph{ID: "order-received-alert", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{textNode("a", "x")}}
	if _, err := h.svc.SaveGraph(t.Context(), h.p, g); err != nil {
		t.Fatalf("a slug id was refused: %v", err)
	}
	if _, err := h.ws.Load(g.ID); err != nil {
		t.Errorf("saved flow does not reload: %v", err)
	}
}

// Timeouts arrive as free integers from every entry path.
func TestAbsurdTimeouts(t *testing.T) {
	h := newHarness(t)
	const minInt = -1 << 62
	cases := map[string]core.Graph{
		"graph timeout -1": func() core.Graph {
			g := graph("gt1", []core.Node{{ID: "a", Module: "delay", Params: map[string]any{"ms": 50}}}, nil)
			g.TimeoutSeconds = -1
			return g
		}(),
		"graph timeout minInt": func() core.Graph {
			g := graph("gt2", []core.Node{{ID: "a", Module: "delay", Params: map[string]any{"ms": 50}}}, nil)
			g.TimeoutSeconds = minInt
			return g
		}(),
		"node timeout -1": graph("nt1", []core.Node{
			{ID: "a", Module: "delay", Params: map[string]any{"ms": 50}, TimeoutSeconds: -1}}, nil),
		"node timeout minInt": graph("nt2", []core.Node{
			{ID: "a", Module: "delay", Params: map[string]any{"ms": 50}, TimeoutSeconds: minInt}}, nil),
	}
	for name, g := range cases {
		t.Run(name, func(t *testing.T) {
			start := time.Now()
			st, err := h.submit(g, 20*time.Second)
			if err != nil {
				t.Logf("submit refused: %v", err)
				return
			}
			t.Logf("status=%s after %s", st, time.Since(start).Round(time.Millisecond))
			if st == statusHung {
				t.Errorf("FINDING: never reached a terminal status")
			}
		})
	}
}
