// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"reflect"
	"testing"
)

func behaviorFixture() Graph {
	return Graph{
		ID:        "f1",
		Tenant:    "t",
		Workspace: "ws",
		Name:      "Nightly report",
		Nodes: []Node{
			{
				ID:       "a",
				Module:   "cron_trigger",
				Params:   map[string]any{"cron": "0 9 * * *"},
				Position: &Position{X: 10, Y: 20},
			},
			{ID: "b", Module: "slack_post", Params: map[string]any{"channel": "#ops"}},
		},
		Edges: []Edge{{From: "a", FromPort: "out", To: "b", ToPort: "in"}},
	}
}

// The whole point of BehaviorEqual: a canvas-only edit is not something to
// publish, so it must not raise the editor's "publish your changes" prompt.
// Each of these used to flip Dirty and then be reported as no change at all
// by the diff view.
func TestBehaviorEqual_IgnoresEditorOnlyEdits(t *testing.T) {
	cases := []struct {
		name string
		edit func(g *Graph)
	}{
		{"moved step", func(g *Graph) { g.Nodes[0].Position = &Position{X: 900, Y: 900} }},
		{"step renamed", func(g *Graph) { g.Nodes[0].Label = "Every morning" }},
		{"position dropped", func(g *Graph) { g.Nodes[0].Position = nil }},
		{"note added", func(g *Graph) {
			g.Frames = []Frame{{ID: "fr1", Title: "Morning path", Width: 360, Height: 240}}
		}},
		{"wire re-routed", func(g *Graph) {
			g.Edges[0].Waypoints = []Position{{X: 5, Y: 5}, {X: 50, Y: 5}}
		}},
		{"paused", func(g *Graph) { g.Disabled = true }},
		{"empty node slice vs nil", func(g *Graph) { g.Nodes = []Node{}; g.Edges = []Edge{} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := behaviorFixture()
			b := behaviorFixture()
			if tc.name == "empty node slice vs nil" {
				a.Nodes, a.Edges = nil, nil
			}
			tc.edit(&b)
			if !BehaviorEqual(a, b) {
				t.Errorf("%s should not count as a publishable change", tc.name)
			}
		})
	}
}

func TestBehaviorEqual_CatchesRealChanges(t *testing.T) {
	cases := []struct {
		name string
		edit func(g *Graph)
	}{
		{"param edited", func(g *Graph) { g.Nodes[0].Params["cron"] = "0 3 * * *" }},
		{"module swapped", func(g *Graph) { g.Nodes[1].Module = "email_send" }},
		{"step added", func(g *Graph) { g.Nodes = append(g.Nodes, Node{ID: "c", Module: "delay"}) }},
		{"step removed", func(g *Graph) { g.Nodes = g.Nodes[:1] }},
		{"step switched off", func(g *Graph) { g.Nodes[1].Disabled = true }},
		{"step made non-critical", func(g *Graph) { g.Nodes[1].ContinueOnError = true }},
		{"breakpoint set", func(g *Graph) { g.Nodes[1].Breakpoint = true }},
		{"node timeout set", func(g *Graph) { g.Nodes[1].TimeoutSeconds = 30 }},
		{"edge rewired", func(g *Graph) { g.Edges[0].ToPort = "other" }},
		{"edge error policy", func(g *Graph) { g.Edges[0].OnError = OnErrorFallback }},
		{"trigger added", func(g *Graph) {
			g.Triggers = append(g.Triggers, GraphTrigger{Type: "cron", Cron: "* * * * *"})
		}},
		{"renamed", func(g *Graph) { g.Name = "Weekly report" }},
		{"visibility", func(g *Graph) { g.Visibility = VisibilityPrivate }},
		{"graph timeout", func(g *Graph) { g.TimeoutSeconds = 600 }},
		{"failure notify", func(g *Graph) {
			g.FailureNotify = &FailureNotify{Webhook: "https://hooks.example/x"}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := behaviorFixture()
			b := behaviorFixture()
			tc.edit(&b)
			if BehaviorEqual(a, b) {
				t.Errorf("%s must count as a publishable change", tc.name)
			}
		})
	}
}

// The caller's graph is usually one it is still using — stripping must not
// reach into the slices it handed over.
func TestBehaviorEqual_DoesNotMutateInput(t *testing.T) {
	a := behaviorFixture()
	a.Frames = []Frame{{ID: "fr1"}}
	a.Edges[0].Waypoints = []Position{{X: 1, Y: 2}}
	before := behaviorFixture()
	before.Frames = []Frame{{ID: "fr1"}}
	before.Edges[0].Waypoints = []Position{{X: 1, Y: 2}}

	BehaviorEqual(a, behaviorFixture())

	if !reflect.DeepEqual(a, before) {
		t.Fatalf("BehaviorEqual mutated its argument:\n got %+v\nwant %+v", a, before)
	}
}

// A field added to Graph or Node is a decision: does publishing it change
// what the live flow does? BehaviorEqual's DeepEqual answers "yes" by
// default, which is the safe direction but not always the right one — and
// web/src/lib/diffGraphs.ts has to itemize whatever counts, or the editor
// goes back to prompting for a change its diff view calls no change.
//
// This test fails when the shape changes so that decision gets made, in both
// places, instead of being inherited by accident.
func TestBehaviorEqual_FieldSetIsReviewed(t *testing.T) {
	want := map[string][]string{
		"Graph": {
			"ID", "Version", "Tenant", "Workspace", "Nodes", "Edges", "Triggers",
			"Frames", "Name", "Icon", "Description", "Visibility", "Owner",
			"FailureNotify", "TimeoutSeconds", "Disabled",
			// Graph-level ContinueOnError is read nowhere in the daemon or the
			// engine (the flag that works is Node.ContinueOnError). It stays
			// counted as behaviour: the safe answer for a field whose meaning
			// nobody has settled.
			"ContinueOnError",
		},
		"Node": {
			"ID", "Module", "Params", "Env", "Label", "Position", "TimeoutSeconds",
			"Breakpoint", "Disabled", "ContinueOnError",
		},
		"Edge": {"From", "FromPort", "To", "ToPort", "OnError", "Waypoints"},
	}
	got := map[string][]string{
		"Graph": fieldNames(Graph{}),
		"Node":  fieldNames(Node{}),
		"Edge":  fieldNames(Edge{}),
	}
	for name, wantFields := range want {
		if !reflect.DeepEqual(got[name], wantFields) {
			t.Errorf("core.%s fields changed.\n got %v\nwant %v\n\n"+
				"Decide whether the new field is publishable behaviour (leave it in "+
				"BehaviorEqual and itemize it in web/src/lib/diffGraphs.ts) or "+
				"editor-only (clear it in stripCosmetic, on both sides), then update "+
				"this list.", name, got[name], wantFields)
		}
	}
}

func fieldNames(v any) []string {
	rt := reflect.TypeOf(v)
	out := make([]string, 0, rt.NumField())
	for i := 0; i < rt.NumField(); i++ {
		out = append(out, rt.Field(i).Name)
	}
	return out
}
