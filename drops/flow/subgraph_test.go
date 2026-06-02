package flow

import (
	"encoding/json"
	"testing"

	"git.sr.ht/~klahr/hazyflow/core"
)

func TestSubgraph_RejectsMissingGraphID(t *testing.T) {
	job := core.Job{Params: map[string]any{}}
	res, _ := executeSubgraph(t.Context(), job, nil)
	if res.Status != core.StatusError {
		t.Fatalf("status = %q, want error", res.Status)
	}
	if res.Error == nil || res.Error.Code != "bad_param" {
		t.Errorf("error = %+v", res.Error)
	}
}

func TestSubgraph_EmitsAwaitingWithMetadata(t *testing.T) {
	job := core.Job{
		Params: map[string]any{
			"graph_id": "child-flow",
			"input_map": map[string]any{
				"in": "seed_node",
			},
			"output_map": map[string]any{
				"result": map[string]any{
					"node": "final",
					"port": "out",
				},
			},
		},
		Input: map[string]core.Ref{
			"in": {MIME: "application/json", Inline: map[string]any{"x": 1}},
		},
	}
	res, err := executeSubgraph(t.Context(), job, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusAwaiting {
		t.Fatalf("status = %q, want awaiting", res.Status)
	}
	if got, _ := res.Output["pending_child_graph_id"].Inline.(string); got != "child-flow" {
		t.Errorf("graph_id = %q", got)
	}

	// Seeds payload should embed the parent input under the child
	// node ID, keyed by "in" port.
	seedsJSON, _ := res.Output["pending_input_seeds"].Inline.(string)
	var seeds map[string]core.Result
	if err := json.Unmarshal([]byte(seedsJSON), &seeds); err != nil {
		t.Fatalf("unmarshal seeds: %v", err)
	}
	seedNode := seeds["seed_node"]
	ref, ok := seedNode.Output["in"]
	if !ok {
		t.Fatalf("seeds[seed_node].in missing: %+v", seedNode)
	}
	body, _ := ref.Inline.(map[string]any)
	if body["x"].(float64) != 1 {
		t.Errorf("seed value = %+v", body)
	}

	// Output map should round-trip through JSON.
	outMapJSON, _ := res.Output["pending_output_map"].Inline.(string)
	var bindings map[string]SubgraphOutputBinding
	if err := json.Unmarshal([]byte(outMapJSON), &bindings); err != nil {
		t.Fatalf("unmarshal output_map: %v", err)
	}
	if got := bindings["result"]; got.Node != "final" || got.Port != "out" {
		t.Errorf("bindings[result] = %+v", got)
	}
}

func TestSubgraph_HandlesEmptyMaps(t *testing.T) {
	job := core.Job{Params: map[string]any{"graph_id": "g"}}
	res, _ := executeSubgraph(t.Context(), job, nil)
	if res.Status != core.StatusAwaiting {
		t.Fatalf("status = %q", res.Status)
	}
	// Empty seeds + empty output_map JSON are both valid.
	if seedsJSON, _ := res.Output["pending_input_seeds"].Inline.(string); seedsJSON != "{}" {
		t.Errorf("seeds = %q, want {}", seedsJSON)
	}
}

func TestSubgraph_RejectsMalformedOutputMap(t *testing.T) {
	job := core.Job{Params: map[string]any{
		"graph_id": "g",
		"output_map": map[string]any{
			"result": map[string]any{"node": "x"}, // missing port
		},
	}}
	res, _ := executeSubgraph(t.Context(), job, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Fatalf("res = %+v", res)
	}
}
