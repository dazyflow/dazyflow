// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import "testing"

func TestWithPassthrough_PrependsPortsOnProcessingDrops(t *testing.T) {
	m := Manifest{
		ID:      "x",
		Inputs:  []Port{{Port: "in"}},
		Outputs: []Port{{Port: "out"}},
	}
	got := WithPassthrough(m)
	if got.Inputs[0].Port != PassPort || got.Outputs[0].Port != PassPort {
		t.Fatalf("pass port not prepended: in=%v out=%v", got.Inputs, got.Outputs)
	}
	if len(got.Inputs) != 2 || got.Inputs[1].Port != "in" {
		t.Errorf("original inputs not preserved after pass: %v", got.Inputs)
	}
	// Untyped (wildcard) and not required so it connects to anything.
	if p, _ := got.Input(PassPort); p.Required || len(p.MIME) != 0 {
		t.Errorf("pass port should be optional + untyped, got %+v", p)
	}
}

func TestWithPassthrough_SkipsTriggers(t *testing.T) {
	// A trigger originates a flow from an external event — nothing upstream
	// to thread from, so no pass pin. Detected by ExecutionTrigger or the
	// "trigger" category; assert both signals independently.
	for _, m := range []Manifest{
		{ID: "t1", ExecutionModel: ExecutionTrigger, Outputs: []Port{{Port: "out"}}},
		{ID: "t2", Category: "trigger", Outputs: []Port{{Port: "out"}}},
	} {
		got := WithPassthrough(m)
		if _, ok := got.Input(PassPort); ok {
			t.Errorf("%s: trigger should not gain a pass input", m.ID)
		}
		if _, ok := got.Output(PassPort); ok {
			t.Errorf("%s: trigger should not gain a pass output", m.ID)
		}
	}
}

func TestWithPassthrough_SkipsValueSource(t *testing.T) {
	// A literal value source (Text, Number) opts out via ValueSource: its
	// output is authored in a param, not wired in, so no pass pin.
	m := Manifest{ID: "text", ValueSource: true, Outputs: []Port{{Port: "out"}}}
	got := WithPassthrough(m)
	if _, ok := got.Input(PassPort); ok {
		t.Errorf("value source should not gain a pass input")
	}
	if _, ok := got.Output(PassPort); ok {
		t.Errorf("value source should not gain a pass output")
	}
}

func TestWithPassthrough_AddsToInputlessAction(t *testing.T) {
	// An action that takes no DATA input but configures from params sits
	// mid-flow and must thread a value — so it gets the pass pin even with
	// zero declared inputs. This holds whether it has no params at all (Slack
	// "list channels") or required config params (a DB query's dsn/sql, a
	// fetcher's owner/repo): required params alone do NOT make it a value
	// source — only the explicit ValueSource flag does.
	for _, m := range []Manifest{
		{ID: "slack_list_channels", Category: "network", Outputs: []Port{{Port: "channels"}}},
		{ID: "github_list_issues", Category: "network", Outputs: []Port{{Port: "issues"}}},
	} {
		got := WithPassthrough(m)
		if _, ok := got.Input(PassPort); !ok {
			t.Errorf("%s: input-less action should gain a pass input, got %v", m.ID, got.Inputs)
		}
		if _, ok := got.Output(PassPort); !ok {
			t.Errorf("%s: input-less action should gain a pass output, got %v", m.ID, got.Outputs)
		}
	}

	// And the pass pin is prepended ahead of the real output.
	m := Manifest{ID: "slack_list_channels", Category: "network", Outputs: []Port{{Port: "channels"}}}
	got := WithPassthrough(m)
	if p, ok := got.Input(PassPort); !ok || p.Port != PassPort {
		t.Errorf("input-less action should gain a pass input, got inputs=%v", got.Inputs)
	}
	if _, ok := got.Output(PassPort); !ok {
		t.Errorf("input-less action should gain a pass output, got outputs=%v", got.Outputs)
	}
	if got.Outputs[0].Port != PassPort || got.Outputs[1].Port != "channels" {
		t.Errorf("pass should be prepended ahead of the real output: %v", got.Outputs)
	}
}

func TestWithPassthrough_Idempotent(t *testing.T) {
	m := WithPassthrough(Manifest{Inputs: []Port{{Port: "in"}}, Outputs: []Port{{Port: "out"}}})
	again := WithPassthrough(m)
	if len(again.Inputs) != len(m.Inputs) || len(again.Outputs) != len(m.Outputs) {
		t.Errorf("WithPassthrough not idempotent: %d/%d vs %d/%d",
			len(again.Inputs), len(again.Outputs), len(m.Inputs), len(m.Outputs))
	}
}

func TestApplyPassthrough_CopiesInputToOutputOnSuccess(t *testing.T) {
	in := map[string]Ref{PassPort: {MIME: "text/plain", Inline: "ctx-42"}}
	res := &Result{Status: StatusOK, Output: map[string]Ref{"out": {Inline: "computed"}}}
	ApplyPassthrough(in, res)
	if res.Output[PassPort].Inline != "ctx-42" {
		t.Errorf("pass value not threaded to output: %+v", res.Output[PassPort])
	}
	if res.Output["out"].Inline != "computed" {
		t.Errorf("real output clobbered: %+v", res.Output["out"])
	}
}

func TestApplyPassthrough_NoopWhenNotOKorNoPass(t *testing.T) {
	// No pass input → nothing added.
	res := &Result{Status: StatusOK, Output: map[string]Ref{}}
	ApplyPassthrough(map[string]Ref{}, res)
	if _, ok := res.Output[PassPort]; ok {
		t.Errorf("pass output added without a pass input")
	}
	// Failed node → the chain breaks, no passthrough.
	res = &Result{Status: StatusError}
	ApplyPassthrough(map[string]Ref{PassPort: {Inline: "x"}}, res)
	if res.Output[PassPort].Inline != nil {
		t.Errorf("failed node should not emit pass output")
	}
}

func TestApplyPassthrough_DoesNotOverrideNodesOwnPass(t *testing.T) {
	in := map[string]Ref{PassPort: {Inline: "threaded"}}
	res := &Result{Status: StatusOK, Output: map[string]Ref{PassPort: {Inline: "node-set"}}}
	ApplyPassthrough(in, res)
	if res.Output[PassPort].Inline != "node-set" {
		t.Errorf("should not override a node's own pass output: %+v", res.Output[PassPort])
	}
}

func TestMarkListPorts_TagsListNamedPortsOnly(t *testing.T) {
	m := Manifest{
		ID: "x",
		Inputs: []Port{
			{Port: "text"}, // scalar/per-item — must stay unmarked
			{Port: "rows"}, // list input
		},
		Outputs: []Port{
			{Port: "responses"}, // list output
			{Port: "summary"},   // scalar output
		},
	}
	got := MarkListPorts(m)
	if got.Inputs[0].List {
		t.Error("text input should not be marked List")
	}
	if !got.Inputs[1].List {
		t.Error("rows input should be marked List")
	}
	if !got.Outputs[0].List {
		t.Error("responses output should be marked List")
	}
	if got.Outputs[1].List {
		t.Error("summary output should not be marked List")
	}
	// Must not mutate the caller's slices (registry safety).
	if m.Outputs[0].List {
		t.Error("MarkListPorts mutated the input manifest's ports")
	}
}
