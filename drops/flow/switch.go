// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/cel-go/cel"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/internal/rowcel"
)

// Switch is the N-way value router — the multi-case sibling of Branch. It
// forwards its payload down one of N case ports by matching a key against each
// case's value (first-match-wins), with everything unmatched landing on
// `default`. Use it instead of chaining Branches when you fan one payload out by
// a status/enum/category.
//
// A case says what it wants in one of two ways. `equals` is the plain one, and
// reuses Compare's evaluator (looseEqual / inSet) so Switch can never drift
// from Compare's equality semantics: a list matches any element, a scalar
// matches by loose equality. `filter` is the richer one — the same visual
// row-condition the Find, Split rows and Route rows steps show, compiled by
// internal/rowcel — for the cases equality cannot state: a threshold, a
// combination, a field that must merely exist.
//
// The incoming value is bound to `row` for a condition, so the builder's own
// output (row.<field> op value) works unchanged. A value that is not an object
// is wrapped as {"value": …}, which is the only way a builder that speaks in
// fields can say anything about a bare number.
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
			Description: "Send one value down a different path depending on what it is — the multi-way version of Branch. Where Branch has a yes and a no, Switch has up to eight matches, and whatever comes in leaves on the FIRST one it satisfies. Anything satisfying none of them leaves on Everything else.\n\nEach match says what it wants in one of two ways. Give it a value to look for and the path is taken by anything equal to it — one value (\"paid\", 200, true), or a list ([200,201,204]) if several should all go the same way. Or set a condition instead, with the same editor the row steps use, for what equality cannot say: amount over 100, status paid AND country SE, a field that merely has to be there.\n\nBy default the whole incoming value is what a value-match compares. Set \"What to match on\" to compare just one field of it instead — an order's status, say. Either way the whole order travels onward; this only decides which path it takes. A condition always sees the whole value, as `row` — so `row.status == 'paid' && row.amount > 100`. When what comes in is a plain number or text rather than an object, a condition reads it as `row.value`.\n\nFirst match wins, so if two of them could apply, the earlier one takes it — put the strict match before the loose one, or the loose one swallows what the strict one was meant to catch.",
			Summary:     "Route the input payload to one of N case ports by matching a key against each case value; unmatched goes to default.",
			Examples: []core.ParamsExample{
				{
					Title:  "Route an order by status",
					Params: json.RawMessage(`{"field":"status","cases":[{"slot":"case_1","equals":"paid"},{"slot":"case_2","equals":"refunded"},{"slot":"case_3","equals":"failed"}]}`),
					Notes:  "Connect the order into 'in'. A paid order rides out case_1; anything not paid/refunded/failed goes to default. The whole order travels — field only selects what to match on.",
				},
				{
					Title:  "Route by a condition, not just a value",
					Params: json.RawMessage(`{"cases":[{"slot":"case_1","filter":"row.status == 'paid' && row.amount > 100"},{"slot":"case_2","filter":"row.status == 'paid'"},{"slot":"case_3","filter":"row.status == 'refunded'"}]}`),
					Notes:  "The strict match comes first: a paid order over 100 takes case_1, and only a smaller paid one falls through to case_2.",
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
								"equals":{"title":"Value to look for","description":"What has to be equal for this path to be taken — one value (\"paid\", 200, true), or a list ([200,201,204]) if several values should all take the same path. Use a condition instead when equality cannot say what you mean."},
								"filter":{"type":"string","format":"row-condition","title":"Condition","description":"The condition that has to hold for this path to be taken — row.amount > 100, or row.status == 'paid' && row.country == 'SE'. The incoming value is 'row'; a plain number or text reads as row.value. Set this OR a value to look for, not both."}
							},
							"required":["slot"]
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
	// Exactly one of equals/filter is set; prog is the compiled filter.
	hasEquals bool
	filter    string
	prog      cel.Program
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
	// A condition always sees the whole value, whatever `field` says: `field`
	// picks what a VALUE-match compares, and a condition names its own fields.
	scope := conditionScope(payload.Inline)

	slot := switchDefaultSlot
	for i, c := range cases {
		var hit bool
		if c.hasEquals {
			hit = matchCase(key, c.equals)
		} else {
			ok, err := rowcel.EvalBool(c.prog, scope)
			if err != nil {
				return params.Err(job, "eval",
					fmt.Sprintf("cases[%d] (slot %q): %v", i, c.slot, err)), nil
			}
			hit = ok
		}
		if hit {
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

// conditionScope is what a condition sees. An object is itself; anything else
// is wrapped, so the row-condition builder — which can only write row.<field>
// — can still say something about a bare number or a list.
func conditionScope(payload any) map[string]any {
	if m, ok := payload.(map[string]any); ok {
		return m
	}
	return map[string]any{"value": payload}
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
		_, hasEquals := obj["equals"]
		filter, _ := obj["filter"].(string)
		filter = strings.TrimSpace(filter)
		switch {
		case hasEquals && filter != "":
			return nil, fmt.Errorf("cases[%d] (slot %q): has both a value to look for and a condition — keep the one you meant", i, slot)
		case !hasEquals && filter == "":
			return nil, fmt.Errorf("cases[%d] (slot %q): needs a value to look for or a condition", i, slot)
		}
		c := switchCase{slot: slot, hasEquals: hasEquals, filter: filter}
		if hasEquals {
			c.equals = coerceLiteral(obj["equals"])
		} else {
			env, err := rowcel.Env()
			if err != nil {
				return nil, fmt.Errorf("cases[%d] (slot %q): %v", i, slot, err)
			}
			prog, err := rowcel.Compile(env, filter, "condition")
			if err != nil {
				return nil, fmt.Errorf("cases[%d] (slot %q): %v", i, slot, err)
			}
			c.prog = prog
		}
		cases = append(cases, c)
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
