// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package flow

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/engine"
)

// Switch is the N-way value router — the multi-case sibling of Branch. It
// forwards its payload down one of N case ports by matching a key against each
// case's value (first-match-wins), with everything unmatched landing on
// `default`. Use it instead of chaining Branches when you fan one payload out by
// a status/enum/category.
//
// Matching reuses Compare's evaluator (looseEqual / inSet), so Switch can never
// drift from Compare's equality semantics: a case `equals` that is a list
// matches any element, a scalar matches by loose equality.
//
// Output slots are fixed (`case_1..case_8` + `default`) for the same reason as
// route_rows: variadic-by-name output handles need editor support that isn't
// here yet. The upgrade to semantic names is purely additive.

const switchSlotCount = 8

const switchDefaultSlot = "default"

func init() {
	outputs := make([]core.Port, 0, switchSlotCount+1)
	for i := 1; i <= switchSlotCount; i++ {
		outputs = append(outputs, core.Port{
			Port:  fmt.Sprintf("case_%d", i),
			Label: fmt.Sprintf("Match %d", i),
		})
	}
	outputs = append(outputs, core.Port{Port: switchDefaultSlot, Label: "Everything else"})

	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "switch",
			Version:     "1.0",
			Label:       "Switch",
			Icon:        "split",
			Category:    "flow_control",
			Provider:    "internal",
			Tags:        []string{"conditional", "routing", "switch", "case", "multiway"},
			Description: "Send one value down a different path depending on what it is — the multi-way version of Branch. Where Branch has a yes and a no, Switch has up to eight matches: you give each one a value to look for, and whatever comes in leaves on the first match whose value it equals. Anything equal to none of them leaves on Everything else.\n\nBy default the whole incoming value is what gets matched. Set \"What to match on\" to compare just one field of it instead — an order's status, say — and the whole order still travels onward; the field only decides which path it takes. A match can also hold a list of values, and then anything equal to ANY of them takes that path (200, 201 and 204 all going one way).\n\nFirst match wins, so if two of them could apply, the earlier one takes it. Reach for this instead of chaining Branch steps when you are fanning one payload out by a status, a category or a type.",
			Summary:     "Route the input payload to one of N case ports by matching a key against each case value; unmatched goes to default.",
			Examples: []core.ParamsExample{
				{
					Title:  "Route an order by status",
					Params: json.RawMessage(`{"field":"status","cases":[{"slot":"case_1","equals":"paid"},{"slot":"case_2","equals":"refunded"},{"slot":"case_3","equals":"failed"}]}`),
					Notes:  "Connect the order into 'in'. A paid order rides out case_1; anything not paid/refunded/failed goes to default. The whole order travels — field only selects what to match on.",
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
						"title":"Matches",
						"description":"The matches, in order. Whatever comes in leaves on the FIRST one whose value it equals; if it equals none of them it leaves on Everything else. Order matters when two could apply — the earlier one takes it.",
						"items":{
							"type":"object",
							"properties":{
								"slot":{"type":"string","title":"Path to send it down","description":"Which output it leaves on when this match wins. \"case_1\" is the pin labelled Match 1, \"case_2\" is Match 2, and so on up to \"case_8\"."},
								"equals":{"title":"Value to look for","description":"What has to be equal for this path to be taken — one value (\"paid\", 200, true), or a list ([200,201,204]) if several values should all take the same path."}
							},
							"required":["slot","equals"]
						}
					},
					"field":{"type":"string","title":"What to match on","description":"Which field of the incoming value to compare — status, say, or customer.country for a field inside a field. Leave it empty to compare the whole value. Either way the whole value travels onward; this only decides which path it takes.","x_advanced":true}
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

func validCaseSlots() map[string]struct{} {
	out := make(map[string]struct{}, switchSlotCount)
	for i := 1; i <= switchSlotCount; i++ {
		out[fmt.Sprintf("case_%d", i)] = struct{}{}
	}
	return out
}
