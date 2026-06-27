// SPDX-FileCopyrightText: 2026 Joachim Klahr
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

// Switch is the N-way value router — the multi-case sibling of Branch. Branch
// forwards its payload down one of two ports by a boolean; Switch forwards it
// down one of N case ports by matching a key against each case's value
// (first-match-wins, à la a switch/case statement), with everything unmatched
// landing on `default`. Use it instead of chaining Branches when you fan one
// payload out by a status/enum/category.
//
// Matching reuses Compare's evaluator (looseEqual / inSet from compare.go,
// same package): a case `equals` that's a list matches if the key equals ANY
// element (one_of semantics); a scalar matches by loose equality. So Switch
// can never drift from Compare's equality semantics — it IS Compare's match,
// fanned across cases.
//
// Output slots are fixed (`case_1..case_8` + `default`) for the same reason as
// route_rows: variadic-by-name output handles need editor support that isn't
// here yet. The upgrade to semantic names is purely additive.

// switchSlotCount is how many named case outputs the manifest declares —
// matched to route_rows' slot count for consistency. Cases beyond this either
// fold into `default` or compose two Switches.
const switchSlotCount = 8

// switchDefaultSlot is the catch-all port for a key that matches no case.
const switchDefaultSlot = "default"

func init() {
	outputs := make([]core.Port, 0, switchSlotCount+1)
	for i := 1; i <= switchSlotCount; i++ {
		outputs = append(outputs, core.Port{
			Port:  fmt.Sprintf("case_%d", i),
			Label: fmt.Sprintf("Case %d", i),
		})
	}
	outputs = append(outputs, core.Port{Port: switchDefaultSlot, Label: "Default"})

	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "switch",
			Version:     "1.0",
			Label:       "Switch",
			Icon:        "split",
			Category:    "flow_control",
			Provider:    "internal",
			Tags:        []string{"conditional", "routing", "switch", "case", "multiway"},
			Description: "Route the payload on `in` to one of N case ports by matching a key against each case's value. Param `cases` is an ordered list of {slot, equals} — the FIRST case whose value matches the key wins and the whole payload rides out that slot; a key matching no case goes to `default`. Match the whole input, or a field of it via the `field` param. An `equals` that's a list matches if the key equals any element (like Compare's one_of). The multi-way sibling of Branch — reach for it instead of chaining Branches to fan one payload out by status/enum/category.",
			Summary:     "Route the input payload to one of N case ports by matching a key against each case value; unmatched goes to default.",
			Examples: []core.ParamsExample{
				{
					Title:  "Route an order by status",
					Params: json.RawMessage(`{"field":"status","cases":[{"slot":"case_1","equals":"paid"},{"slot":"case_2","equals":"refunded"},{"slot":"case_3","equals":"failed"}]}`),
					Notes:  "Wire the order into 'in'. A paid order rides out case_1; anything not paid/refunded/failed goes to default. The whole order travels — field only selects what to match on.",
				},
				{
					Title:  "Group HTTP statuses, match-any per case",
					Params: json.RawMessage(`{"cases":[{"slot":"case_1","equals":[200,201,204]},{"slot":"case_2","equals":[400,404,422]}]}`),
					Notes:  "No field: the whole 'in' value is the key. A list 'equals' matches if the key is any of its elements. 500 matches neither and goes to default.",
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{{
				Port:     "in",
				Required: true,
				Label:    "Value",
			}},
			Outputs: outputs,
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"cases":{
						"type":"array",
						"title":"Cases",
						"description":"Ordered list of {slot, equals}. The FIRST case whose value matches the key wins; the payload rides out that slot. Unmatched payloads go to default.",
						"items":{
							"type":"object",
							"properties":{
								"slot":{"type":"string","title":"Slot","description":"Output port name. One of case_1..case_8."},
								"equals":{"title":"Equals","description":"Value to match the key against — a literal (\"paid\", 200, true) or a list ([200,201,204]) to match any element."}
							},
							"required":["slot","equals"]
						}
					},
					"field":{"type":"string","title":"Key field","description":"Optional dot-path into the input to match on (e.g. status). Empty matches the whole input value. The full payload rides downstream regardless.","x_advanced":true}
				},
				"required":["cases"]
			}`),
			Idempotent: true,
			// Pure router (see Branch): the universal pass pin would emit on every
			// success regardless of which case was taken, firing a node wired to
			// it on EVERY path and defeating the routing. Route via in → case_N.
			NoPassthrough: true,
		},
		Execute: executeSwitch,
	})
}

// switchCase is one parsed case rule: the output slot and the already-coerced
// value the key is matched against.
type switchCase struct {
	slot   string
	equals any
}

// executeSwitch matches the key (the whole `in` payload, or the field of it
// named by params.field) against each case in order and forwards the payload
// out the first matching slot — or `default` when nothing matches. Exactly one
// output port is ever set, so downstream edges fork on the decision by port
// presence, the same mechanism Branch's then/else uses.
func executeSwitch(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	cases, err := parseSwitchCases(job.Params)
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}

	payload, ok := job.Input["in"]
	if !ok {
		return params.Err(job, "missing_input", "input port 'in' is required"), nil
	}

	field, _ := job.Params["field"].(string)
	key, err := extractPath(payload.Inline, field)
	if err != nil {
		return params.Err(job, "bad_input", err.Error()), nil
	}

	slot := switchDefaultSlot
	for _, c := range cases {
		if matchCase(key, c.equals) {
			slot = c.slot
			break // first-match-wins
		}
	}

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{slot: payload},
	}, nil
}

// matchCase reports whether the key matches a case value. A list value matches
// if the key loosely equals any element (one_of); a scalar matches by loose
// equality. Both delegate to compare.go so the semantics are Compare's.
func matchCase(key, equals any) bool {
	if arr, ok := equals.([]any); ok {
		matched, _ := inSet(key, arr)
		return matched
	}
	return looseEqual(key, equals)
}

// parseSwitchCases validates params.cases. Each slot must be one of the
// manifest's declared case_N ports (a typo would otherwise route silently to a
// nonexistent port and look like a default match). The `equals` value is run
// through coerceLiteral so a typed-in "200" becomes the number 200 — the same
// leniency Compare's A/B literals get.
func parseSwitchCases(p map[string]any) ([]switchCase, error) {
	raw, ok := p["cases"]
	if !ok {
		return nil, fmt.Errorf("cases: required (ordered list of {slot, equals})")
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("cases: expected array, got %T", raw)
	}
	if len(arr) == 0 {
		return nil, fmt.Errorf("cases: at least one case required (otherwise everything routes to default)")
	}
	valid := validCaseSlots()
	cases := make([]switchCase, 0, len(arr))
	for i, item := range arr {
		obj, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("cases[%d]: expected object, got %T", i, item)
		}
		slot, _ := obj["slot"].(string)
		if slot == "" {
			return nil, fmt.Errorf("cases[%d]: missing or empty 'slot'", i)
		}
		if _, ok := valid[slot]; !ok {
			return nil, fmt.Errorf("cases[%d]: slot %q is not a known output port (use one of case_1..case_%d)", i, slot, switchSlotCount)
		}
		if _, has := obj["equals"]; !has {
			return nil, fmt.Errorf("cases[%d] (slot %q): missing 'equals'", i, slot)
		}
		cases = append(cases, switchCase{slot: slot, equals: coerceLiteral(obj["equals"])})
	}
	return cases, nil
}

// validCaseSlots returns the set of legal case_N output port names.
func validCaseSlots() map[string]struct{} {
	out := make(map[string]struct{}, switchSlotCount)
	for i := 1; i <= switchSlotCount; i++ {
		out[fmt.Sprintf("case_%d", i)] = struct{}{}
	}
	return out
}
