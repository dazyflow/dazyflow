// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon_test

import (
	"strings"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/daemon"
)

// The JOIN, through a real worker and a real engine.
//
// The unit tests around templateRefs/addTemplateResults all pass with the
// worker's call to them deleted, because they call those functions directly.
// That is the same gap that produced the bug in the first place: every layer
// covered, and nothing covering the wire between them. This one submits a
// graph and reads what the node actually produced.
//
//	text(a) ─▶ delay(b) ─▶ render_template(c)
//
// c's only predecessor is b, and its template names a — two hops up. Before
// the fix that failed the node outright with "no result recorded for node a".
func TestPerNode_UpstreamResolvesBeyondDirectPredecessors(t *testing.T) {
	t.Parallel()
	h := newWorkerHarness(t, 1)

	g := core.Graph{
		ID: "refs", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "a", Module: "text", Params: map[string]any{"text": "0.27.5"}},
			{ID: "b", Module: "delay", Params: map[string]any{"ms": 1}},
			{ID: "c", Module: "render_template", Params: map[string]any{
				"template": "Version ${upstream.a.out} shipped",
			}},
		},
		Edges: []core.Edge{
			{From: "a", FromPort: "out", To: "b", ToPort: "pass"},
			{From: "b", FromPort: "pass", To: "c", ToPort: "pass"},
		},
	}
	graphRunID, err := h.svc.SubmitGraph(t.Context(), h.principal, g)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	terminal := waitForTerminalEvent(t, h.bus, h.jobs, graphRunID, 10*time.Second)
	if terminal.Status != core.JobStatusSucceeded {
		t.Fatalf("run status = %q (a reference two hops up used to fail the node)", terminal.Status)
	}

	rec, err := h.jobs.Get(t.Context(), daemon.NodeJobID(graphRunID, "c"))
	if err != nil {
		t.Fatalf("Get c: %v", err)
	}
	if rec.Result == nil {
		t.Fatal("c produced no result")
	}
	var rendered string
	for _, ref := range rec.Result.Output {
		if s, ok := ref.Inline.(string); ok && strings.Contains(s, "Version") {
			rendered = s
			break
		}
	}
	if !strings.Contains(rendered, "0.27.5") {
		t.Errorf("rendered = %q, want the resolved version", rendered)
	}
	if strings.Contains(rendered, "${upstream") {
		t.Errorf("rendered = %q, still carries the raw template", rendered)
	}
}
