// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"reflect"
	"strings"
	"testing"
)

var editManifests = map[string]Manifest{
	"webhook_input":      {ID: "webhook_input", Outputs: []Port{{Port: "body"}}},
	"slack.post_message": {ID: "slack.post_message", Inputs: []Port{{Port: "text"}, {Port: "channel"}}, Outputs: []Port{{Port: "result"}}},
	"filter":             {ID: "filter", Inputs: []Port{{Port: "in"}}, Outputs: []Port{{Port: "kept"}, {Port: "dropped"}}},
}

func strp(s string) *string { return &s }

func TestApplyGraphOps_AddAfterWiresDefaultPortsAndPlaces(t *testing.T) {
	g := Graph{Nodes: []Node{{ID: "hook", Module: "webhook_input", Position: &Position{X: 80, Y: 80}}}}
	next, rep, err := ApplyGraphOps(g, []GraphOp{
		{Op: "add_node", Module: "slack.post_message", After: "hook", Params: map[string]any{"channel": "#ops"}},
		{Op: "add_node", Module: "slack.post_message", After: "hook"},
	}, editManifests)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rep.Added, []string{"post_message", "post_message_2"}) || rep.Connected != 2 {
		t.Fatalf("report = %+v", rep)
	}
	if e := next.Edges[0]; e.From != "hook" || e.FromPort != "body" || e.To != "post_message" || e.ToPort != "text" {
		t.Errorf("edge = %+v, want hook.body → post_message.text", e)
	}
	a, _ := next.Node("post_message")
	b, _ := next.Node("post_message_2")
	if a.Position == nil || a.Position.X != 80+placeColumn || a.Position.Y != 80 {
		t.Errorf("first placed at %+v", a.Position)
	}
	if b.Position == nil || b.Position.X != a.Position.X || b.Position.Y != a.Position.Y+placeRow {
		t.Errorf("sibling placed at %+v, want below %+v", b.Position, a.Position)
	}
	if len(g.Nodes) != 1 || len(g.Edges) != 0 {
		t.Error("ApplyGraphOps modified its input")
	}
}

func TestApplyGraphOps_UpdateMergesAndNullDeletes(t *testing.T) {
	g := Graph{Nodes: []Node{{ID: "post", Module: "slack.post_message", Params: map[string]any{"channel": "#a", "text": "hi"}}}}
	next, rep, err := ApplyGraphOps(g, []GraphOp{
		{Op: "update_node", Node: "post", Label: strp("Tell ops"), Params: map[string]any{"channel": "#ops", "text": nil}},
	}, editManifests)
	if err != nil {
		t.Fatal(err)
	}
	n, _ := next.Node("post")
	if !reflect.DeepEqual(n.Params, map[string]any{"channel": "#ops"}) || n.Label != "Tell ops" {
		t.Errorf("node = %+v", n)
	}
	if !reflect.DeepEqual(rep.Updated, []string{"post"}) {
		t.Errorf("updated = %v", rep.Updated)
	}
	if g.Nodes[0].Params["channel"] != "#a" {
		t.Error("input params were modified")
	}
}

func TestApplyGraphOps_RemoveTakesItsEdges(t *testing.T) {
	g := Graph{
		Nodes: []Node{{ID: "a", Module: "webhook_input"}, {ID: "b", Module: "filter"}, {ID: "c", Module: "slack.post_message"}},
		Edges: []Edge{{From: "a", FromPort: "body", To: "b", ToPort: "in"}, {From: "b", FromPort: "kept", To: "c", ToPort: "text"}},
	}
	next, rep, err := ApplyGraphOps(g, []GraphOp{{Op: "remove_node", Node: "b"}}, editManifests)
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Nodes) != 2 || len(next.Edges) != 0 || rep.Disconnected != 2 {
		t.Errorf("nodes=%d edges=%d rep=%+v", len(next.Nodes), len(next.Edges), rep)
	}
}

// One bad op leaves nothing applied, and the error says which op and why.
func TestApplyGraphOps_IsAllOrNothing(t *testing.T) {
	g := Graph{Nodes: []Node{{ID: "a", Module: "filter"}, {ID: "b", Module: "slack.post_message"}}}
	_, _, err := ApplyGraphOps(g, []GraphOp{
		{Op: "update_node", Node: "a", Label: strp("x")},
		{Op: "connect", From: "a", FromPort: "nope", To: "b"},
	}, editManifests)
	if err == nil || !strings.Contains(err.Error(), "op 2 (connect)") || !strings.Contains(err.Error(), "kept, dropped") {
		t.Fatalf("err = %v", err)
	}
	if g.Nodes[0].Label != "" {
		t.Error("a failed batch changed the input")
	}
}

func TestApplyGraphOps_Refusals(t *testing.T) {
	g := Graph{Nodes: []Node{{ID: "a", Module: "filter"}}}
	for name, op := range map[string]GraphOp{
		"unknown module":   {Op: "add_node", Module: "nope"},
		"duplicate id":     {Op: "add_node", Node: "a", Module: "filter"},
		"bad id":           {Op: "add_node", Node: "1 x", Module: "filter"},
		"missing node":     {Op: "update_node", Node: "zz"},
		"no such edge":     {Op: "disconnect", From: "a", To: "a"},
		"unknown op":       {Op: "rename"},
		"connect to ghost": {Op: "connect", From: "a", To: "ghost"},
	} {
		if _, _, err := ApplyGraphOps(g, []GraphOp{op}, editManifests); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestApplyGraphOps_ConnectIsIdempotentAndSetFlow(t *testing.T) {
	g := Graph{Nodes: []Node{{ID: "a", Module: "filter"}, {ID: "b", Module: "slack.post_message"}}}
	next, rep, err := ApplyGraphOps(g, []GraphOp{
		{Op: "connect", From: "a", To: "b", ToPort: "channel"},
		{Op: "connect", From: "a", To: "b", ToPort: "channel"},
		{Op: "set_flow", Name: strp("Alerts")},
	}, editManifests)
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Edges) != 1 || rep.Connected != 1 || next.Name != "Alerts" || !rep.FlowChanged {
		t.Errorf("edges=%d rep=%+v name=%q", len(next.Edges), rep, next.Name)
	}
}

func TestGraphETag_ChangesWithContent(t *testing.T) {
	g := Graph{ID: "f", Nodes: []Node{{ID: "a", Module: "filter"}}}
	if GraphETag(g) != GraphETag(g) {
		t.Fatal("ETag is not stable")
	}
	h := g
	h.Name = "x"
	if GraphETag(g) == GraphETag(h) {
		t.Error("ETag ignored a change")
	}
}

// A flow created without positions (create_flow) still lays out as a chain
// once an edit places it: upstream first, left to right, no overlaps.
func TestPlaceNewNodes_PlacesUnpositionedChainLeftToRight(t *testing.T) {
	g := Graph{Nodes: []Node{{ID: "hook", Module: "webhook_input"}}}
	next, _, err := ApplyGraphOps(g, []GraphOp{
		{Op: "add_node", Node: "a", Module: "filter", After: "hook"},
		{Op: "add_node", Node: "b", Module: "filter", After: "hook"},
	}, editManifests)
	if err != nil {
		t.Fatal(err)
	}
	pos := map[string]Position{}
	for _, n := range next.Nodes {
		if n.Position == nil {
			t.Fatalf("%s has no position", n.ID)
		}
		pos[n.ID] = *n.Position
	}
	if pos["hook"].X != placeStart || pos["a"].X != placeStart+placeColumn || pos["b"].X != pos["a"].X {
		t.Errorf("columns: %+v", pos)
	}
	if pos["b"].Y-pos["a"].Y < placeRow {
		t.Errorf("siblings overlap: %+v", pos)
	}
}
