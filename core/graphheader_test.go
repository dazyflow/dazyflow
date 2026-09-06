// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"encoding/json"
	"reflect"
	"testing"
)

// richGraph is a flow with every field of Graph and Node set to a distinctive
// non-zero value, so the equality test below actually exercises them. It holds
// one node of each trigger shape the list views read params off, plus ordinary
// steps whose params are the ones a header drops.
func richGraph() Graph {
	node := func(id, module string, params map[string]any) Node {
		return Node{
			ID: id, Module: module, Params: params,
			Env:             map[string]string{"KEY": "value-" + id},
			Label:           "Step " + id,
			Position:        &Position{X: 12, Y: 34},
			TimeoutSeconds:  17,
			Breakpoint:      true,
			Disabled:        true,
			ContinueOnError: true,
		}
	}
	return Graph{
		ID: "flow-1", Version: "v3", Tenant: "acme", Workspace: "main",
		Nodes: []Node{
			node("cron", "cron_trigger", map[string]any{"cron": "*/5 * * * *", "tz": "Europe/Stockholm", "disabled": true}),
			node("poll", "poll_trigger", map[string]any{"interval_seconds": float64(300), "disabled": false}),
			node("form", "google_form_trigger", map[string]any{"interval_seconds": float64(900)}),
			node("hook", "webhook_input", map[string]any{"public_form": true, "secret": "s3cret"}),
			node("event", "slack_on_mention", map[string]any{"channel": "#ops"}),
			// The ordinary steps: their params are what a header elides.
			node("n1", "http_request", map[string]any{
				"url": "https://api.example.com/v1/resource",
				"body": map[string]any{
					"note": "a realistic amount of configuration on every step",
					"idx":  float64(1),
				},
			}),
			node("n2", "transform", map[string]any{"expr": "input.value * 2"}),
		},
		Edges:    []Edge{{From: "cron", FromPort: "pass", To: "n1", ToPort: "pass"}},
		Triggers: []GraphTrigger{{Type: "cron", Cron: "0 9 * * *"}},
		Frames:   []Frame{{}},

		Name: "Order pipeline", Icon: "zap", Description: "Processes orders",
		Visibility: VisibilityPrivate, Owner: "ada@acme.se",
		FailureNotify:   &FailureNotify{},
		TimeoutSeconds:  600,
		Language:        "sv",
		Disabled:        true,
		ContinueOnError: true,
	}
}

// A header must equal a full decode with the non-trigger steps' params
// removed — nothing else may differ. That equality is what lets the flow
// list, the schedules list, the drop-suggestion miner and the visibility
// filter read a header instead of the full flow.
func TestGraphHeaderMatchesFull(t *testing.T) {
	t.Parallel()
	raw, err := json.Marshal(richGraph())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var full Graph
	if err := json.Unmarshal(raw, &full); err != nil {
		t.Fatalf("full decode: %v", err)
	}
	header, err := UnmarshalGraphHeader(raw)
	if err != nil {
		t.Fatalf("header decode: %v", err)
	}

	// The projection's definition, applied to the full decode.
	want := full
	want.Nodes = make([]Node, len(full.Nodes))
	copy(want.Nodes, full.Nodes)
	elided := 0
	for i := range want.Nodes {
		if IsTriggerModule(want.Nodes[i].Module) {
			continue
		}
		if want.Nodes[i].Params != nil {
			elided++
		}
		want.Nodes[i].Params = nil
	}
	if elided == 0 {
		t.Fatal("fixture has no ordinary step with params — the test proves nothing")
	}
	if !reflect.DeepEqual(header, want) {
		t.Errorf("header decode differs from the full decode with ordinary params dropped\n got %+v\nwant %+v", header, want)
	}

	// And the half that matters most: a trigger's params survive, because
	// FlowRunStatusOf and the schedules list read them.
	for _, n := range header.Nodes {
		if IsTriggerModule(n.Module) && n.Params == nil {
			t.Errorf("trigger step %q (%s) lost its params", n.ID, n.Module)
		}
	}
	if FlowRunStatusOf(header) != FlowRunStatusOf(full) {
		t.Errorf("run status from header = %v, from full = %v",
			FlowRunStatusOf(header), FlowRunStatusOf(full))
	}
}

// The equality above is only as good as the fixture, and a field added to
// Graph or Node that nobody sets there would be "equal" by both being zero.
// This fails when that happens, so the person adding the field is told to
// cover it.
func TestGraphHeaderFixtureCoversEveryField(t *testing.T) {
	t.Parallel()
	g := richGraph()
	zeroFields := func(v reflect.Value) []string {
		var out []string
		for i := range v.NumField() {
			f := v.Type().Field(i)
			if !f.IsExported() {
				continue
			}
			if v.Field(i).IsZero() {
				out = append(out, f.Name)
			}
		}
		return out
	}
	if missing := zeroFields(reflect.ValueOf(g)); len(missing) > 0 {
		t.Errorf("richGraph leaves Graph fields unset: %v\n"+
			"Set them, or TestGraphHeaderMatchesFull cannot tell whether "+
			"UnmarshalGraphHeader carries them through.", missing)
	}
	if missing := zeroFields(reflect.ValueOf(g.Nodes[0])); len(missing) > 0 {
		t.Errorf("richGraph leaves Node fields unset: %v\n"+
			"Set them in the node() helper, or the header decode could drop "+
			"them unnoticed.", missing)
	}
}

// A flow whose trigger params are malformed still belongs in the list: the
// full read is what validates a flow, and a sidebar must not lose a row
// because one step is broken.
func TestGraphHeaderToleratesBadTriggerParams(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"id":"f","name":"Broken","nodes":[
	  {"id":"cron","module":"cron_trigger","params":["not","an","object"]},
	  {"id":"n1","module":"http_request","params":{"url":"https://example.com"}}
	]}`)
	g, err := UnmarshalGraphHeader(raw)
	if err != nil {
		t.Fatalf("header decode: %v", err)
	}
	if g.Name != "Broken" || len(g.Nodes) != 2 {
		t.Fatalf("flow did not survive its bad step: %+v", g)
	}
	if g.Nodes[0].Params != nil {
		t.Errorf("unreadable trigger params should be left nil, got %v", g.Nodes[0].Params)
	}
}
