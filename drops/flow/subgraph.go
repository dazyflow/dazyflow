// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package flow

import (
	"context"
	"encoding/json"
	"fmt"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
	"git.sr.ht/~klahr/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "subgraph",
			Version:     "1.0",
			Label:       "Reusable flow",
			Icon:        "square-stack",
			Category:    "flow_control",
			Provider:    "internal",
			Tags:        []string{"compose", "reuse", "subgraph", "nested"},
			Description: "Run another flow (by ID, in the same workspace) as a single step. Inputs on this step are seeded onto specific child steps via input_map; specific child step outputs become this step's outputs via output_map. The worker submits the child asynchronously; the parent is parked until the child terminates.",
			Summary:     "Invoke another flow as a reusable step, seeding its inputs and projecting selected child outputs back up.",
			Examples: []core.ParamsExample{
				{
					Title:  "Call a shared customer-lookup subgraph",
					Params: json.RawMessage(`{"graph_id":"customer_lookup","input_map":{"email":"start_node"},"output_map":{"customer":{"node":"return_node","port":"out"}}}`),
					Notes:  "Parent input 'email' seeds the child's start_node; child return_node.out becomes the parent's 'customer' output.",
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs:         []core.Port{{Port: "in", Label: "Inputs"}},
			Outputs:        []core.Port{{Port: "out", Label: "Outputs"}},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"graph_id":{"type":"string","title":"Flow ID","description":"The ID of the flow to run as a step."},
					"input_map":{
						"type":"object",
						"description":"map of parent input port → child step ID (the step receives the Ref as its only input)",
						"additionalProperties":{"type":"string"}
					},
					"output_map":{
						"type":"object",
						"description":"map of parent output port → {node, port} in the child",
						"additionalProperties":{
							"type":"object",
							"properties":{
								"node":{"type":"string"},
								"port":{"type":"string"}
							},
							"required":["node","port"]
						}
					}
				},
				"required":["graph_id"]
			}`),
			Idempotent:        true,
			SubmitsChildGraph: true,
		},
		Execute: executeSubgraph,
	})
}

// executeSubgraph is purely declarative: it validates params, repackages
// the parent's Input as a seed map keyed by child node ID, and returns
// awaiting with the metadata the worker needs to submit the child. The
// worker hands SubGraphRunner the metadata; this module never touches
// the JobStore or Service directly.
//
// Wire shape:
//
//   - input_map (param): { parentPort → childNodeID }
//     For every entry, the Ref the engine assembled on parentPort is
//     forwarded onto childNodeID as a single-Ref seed (the child node's
//     Execute will read its `in` input).
//
//   - output_map (param): { parentPort → {node, port} }
//     After the child terminates, the dispatcher reads child.node's
//     terminal output[port] and writes it to the parent's parentPort.
//     Unmapped parent ports stay empty.
func executeSubgraph(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	graphID, ok := job.Params["graph_id"].(string)
	if !ok || graphID == "" {
		return params.Err(job, "bad_param", "graph_id is required"), nil
	}

	inputMap, err := parseInputMap(job.Params["input_map"])
	if err != nil {
		return params.Err(job, "bad_param", fmt.Sprintf("input_map: %v", err)), nil
	}
	outputMap, err := parseOutputMap(job.Params["output_map"])
	if err != nil {
		return params.Err(job, "bad_param", fmt.Sprintf("output_map: %v", err)), nil
	}

	// Build the per-child-node seeds from the parent's input map.
	seeds := map[string]core.Result{}
	for parentPort, childNodeID := range inputMap {
		ref, ok := job.Input[parentPort]
		if !ok {
			continue
		}
		seeds[childNodeID] = core.Result{
			Status: core.StatusOK,
			Output: map[string]core.Ref{"in": ref},
		}
	}

	// Encode the metadata the worker needs to submit the child and the
	// dispatcher needs to map outputs on resume. Output map is JSON-
	// encoded because the JobStore round-trips Results through JSON
	// — a typed Go value would lose its shape.
	outputMapJSON, _ := json.Marshal(outputMap)
	seedsJSON, _ := json.Marshal(seeds)

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusAwaiting,
		Output: map[string]core.Ref{
			"pending_child_graph_id": {MIME: "text/plain", Inline: graphID},
			"pending_input_seeds":    {MIME: "application/json", Inline: string(seedsJSON)},
			"pending_output_map":     {MIME: "application/json", Inline: string(outputMapJSON)},
		},
	}, nil
}

func parseInputMap(raw any) (map[string]string, error) {
	if raw == nil {
		return map[string]string{}, nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("must be an object, got %T", raw)
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("entry %q must be a string (child node ID)", k)
		}
		out[k] = s
	}
	return out, nil
}

// SubgraphOutputBinding names a child node and one of its output ports
// that should be projected to a parent output port. Exported (and
// JSON-tagged) so the dispatcher in daemon/ can deserialize the same
// structure the module wrote.
type SubgraphOutputBinding struct {
	Node string `json:"node"`
	Port string `json:"port"`
}

func parseOutputMap(raw any) (map[string]SubgraphOutputBinding, error) {
	if raw == nil {
		return map[string]SubgraphOutputBinding{}, nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("must be an object, got %T", raw)
	}
	out := make(map[string]SubgraphOutputBinding, len(m))
	for parentPort, v := range m {
		entry, ok := v.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%q must be an object {node, port}, got %T", parentPort, v)
		}
		node, _ := entry["node"].(string)
		port, _ := entry["port"].(string)
		if node == "" || port == "" {
			return nil, fmt.Errorf("%q: both node and port are required", parentPort)
		}
		out[parentPort] = SubgraphOutputBinding{Node: node, Port: port}
	}
	return out, nil
}
