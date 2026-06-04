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

func TestWithPassthrough_SkipsSources(t *testing.T) {
	// A source/trigger (no inputs) gets no pass pin — nothing upstream to
	// thread from.
	m := Manifest{ID: "trigger", Outputs: []Port{{Port: "out"}}}
	got := WithPassthrough(m)
	if _, ok := got.Input(PassPort); ok {
		t.Errorf("source should not gain a pass input")
	}
	if _, ok := got.Output(PassPort); ok {
		t.Errorf("source should not gain a pass output")
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
