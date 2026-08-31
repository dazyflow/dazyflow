// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"testing"

	"github.com/dazyflow/dazyflow/core"
)

// A for_each "body" pin owns the entry node and everything downstream of it;
// the items source, the loop node, and nodes fed from results are NOT owned.
func TestLoopBodyOwners(t *testing.T) {
	g := core.Graph{
		Nodes: []core.Node{
			{ID: "read", Module: "sheets_read_range"},
			{ID: "loop", Module: "for_each"},
			{ID: "mail", Module: "gmail_send_email"},
			{ID: "log", Module: "render_text"},
			{ID: "after", Module: "render_text"},
		},
		Edges: []core.Edge{
			{From: "read", FromPort: "rows", To: "loop", ToPort: "items"},
			{From: "loop", FromPort: "body", To: "mail", ToPort: "in"},
			{From: "mail", FromPort: "meta", To: "log", ToPort: "in"},
			{From: "loop", FromPort: "results", To: "after", ToPort: "in"},
		},
	}
	owners := loopBodyOwners(g)
	if owners["mail"] != "loop" || owners["log"] != "loop" {
		t.Errorf("body should be mail+log owned by loop; got %v", owners)
	}
	for _, n := range []string{"read", "loop", "after"} {
		if _, ok := owners[n]; ok {
			t.Errorf("%s should not be loop-owned; got %v", n, owners)
		}
	}
}

// A loop inside another loop's body is rejected at submit time (the body
// runs in-process with no per-item fan-out, so a nested loop would misbehave).
func TestValidateLoopBodies_RejectsNested(t *testing.T) {
	g := core.Graph{
		Nodes: []core.Node{
			{ID: "rows", Module: "rows"},
			{ID: "outer", Module: "for_each"},
			{ID: "inner", Module: "for_each"},
			{ID: "send", Module: "gmail_send_email"},
		},
		Edges: []core.Edge{
			{From: "rows", FromPort: "out", To: "outer", ToPort: "items"},
			{From: "outer", FromPort: "body", To: "inner", ToPort: "items"},
			{From: "inner", FromPort: "body", To: "send", ToPort: "in"},
		},
	}
	if err := validateLoopBodies(g); err == nil {
		t.Fatal("expected nested for_each to be rejected")
	}
}

// A single (non-nested) loop body passes validation, and so does a plain
// graph with no loops.
func TestValidateLoopBodies_AllowsSingleAndNone(t *testing.T) {
	single := core.Graph{
		Nodes: []core.Node{
			{ID: "rows", Module: "rows"},
			{ID: "loop", Module: "for_each"},
			{ID: "send", Module: "gmail_send_email"},
		},
		Edges: []core.Edge{
			{From: "rows", FromPort: "out", To: "loop", ToPort: "items"},
			{From: "loop", FromPort: "body", To: "send", ToPort: "in"},
		},
	}
	if err := validateLoopBodies(single); err != nil {
		t.Errorf("single loop body should be valid, got: %v", err)
	}
	none := core.Graph{
		Nodes: []core.Node{{ID: "a", Module: "render_text"}},
	}
	if err := validateLoopBodies(none); err != nil {
		t.Errorf("loopless graph should be valid, got: %v", err)
	}
}

// No body pin → no owners (an unwired for_each, and ordinary graphs, are
// unaffected — the dispatcher excludes nothing).
func TestLoopBodyOwners_NoBodyPin(t *testing.T) {
	g := core.Graph{
		Nodes: []core.Node{
			{ID: "read", Module: "sheets_read_range"},
			{ID: "loop", Module: "for_each"},
		},
		Edges: []core.Edge{
			{From: "read", FromPort: "rows", To: "loop", ToPort: "items"},
		},
	}
	if owners := loopBodyOwners(g); len(owners) != 0 {
		t.Errorf("no body pin should yield no owners; got %v", owners)
	}
}
