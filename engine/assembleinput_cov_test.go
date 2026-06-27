package engine

import (
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

func TestAssembleInput_Cov(t *testing.T) {
	manifest := core.Manifest{
		ID: "sink",
		Inputs: []core.Port{
			{Port: "items", Variadic: true},
			{Port: "single"},
		},
	}
	graph := core.Graph{
		ID: "g",
		Edges: []core.Edge{
			// Two edges into a variadic port -> distinct variadic keys.
			{From: "a", To: "sink", FromPort: "out", ToPort: "items"},
			{From: "b", To: "sink", FromPort: "out", ToPort: "items"},
			// A normal edge.
			{From: "a", To: "sink", FromPort: "out", ToPort: "single"},
			// Fallback edge: carries no data, skipped.
			{From: "c", To: "sink", FromPort: "out", ToPort: "single", OnError: core.OnErrorFallback},
			// Edge to a different node: skipped.
			{From: "a", To: "other", FromPort: "out", ToPort: "x"},
			// Source has no recorded result: skipped.
			{From: "ghost", To: "sink", FromPort: "out", ToPort: "single"},
			// Source result exists but lacks the named port: skipped.
			{From: "a", To: "sink", FromPort: "absent", ToPort: "single"},
		},
	}
	prior := map[string]core.Result{
		"a": {Output: map[string]core.Ref{"out": {Inline: "A"}}},
		"b": {Output: map[string]core.Ref{"out": {Inline: "B"}}},
		"c": {Output: map[string]core.Ref{"out": {Inline: "C"}}},
	}

	input := assembleInput(graph, "sink", manifest, prior)

	key0 := core.VariadicInputKey("items", 0)
	key1 := core.VariadicInputKey("items", 1)
	if input[key0].Inline != "A" {
		t.Errorf("%s = %v, want A", key0, input[key0].Inline)
	}
	if input[key1].Inline != "B" {
		t.Errorf("%s = %v, want B", key1, input[key1].Inline)
	}
	if input["single"].Inline != "A" {
		t.Errorf("single = %v, want A", input["single"].Inline)
	}
	// Fallback edge from c must not have overwritten single.
	if input["single"].Inline == "C" {
		t.Error("fallback edge should not feed data")
	}
}
