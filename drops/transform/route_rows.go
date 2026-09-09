// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package transform

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/cel-go/cel"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/engine"
)

// routeSlotCount is how many named routing outputs the manifest declares —
// enough for most real-world routing without making the node visually crowded.
// Routes beyond it fold into `default` or compose two route_rows.
//
// Each slot is `rows_<N>`, which is what the user references from params;
// downstream nodes label the semantic meaning.
//
// Fixed slots because true variadic-by-name outputs need editor support to
// render per-name handles, which is the open "Variadic input/output ports"
// follow-up. The upgrade to semantic names is purely additive.
const routeSlotCount = 8

const routeDefaultSlot = "default"

func init() {
	outputs := make([]core.Port, 0, routeSlotCount+2)
	for i := 1; i <= routeSlotCount; i++ {
		outputs = append(outputs, core.Port{
			Port:  fmt.Sprintf("rows_%d", i),
			Label: fmt.Sprintf("Route %d", i),
			MIME:  []string{"application/json"},
			List:  true, // carries rows — name isn't in the auto-list set, so flag it
		})
	}
	outputs = append(outputs,
		core.Port{Port: routeDefaultSlot, Label: "Everything else", MIME: []string{"application/json"}, List: true},
	)

	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "route_rows",
			Version:     "1.0",
			Label:       "Route rows",
			Icon:        "git-branch",
			Category:    "transformation",
			Provider:    "internal",
			Tags:        []string{"transform", "route", "branch", "fork", "etl"},
			Description: "Send each row down a different path depending on what is in it — the fan-out step. You give it an ordered list of rules, each one a condition plus the route it feeds: for every row the FIRST rule it satisfies wins, and the row leaves on that route's output. A row satisfying no rule leaves on Everything else.\n\nUse it to split one list into per-category paths — SE, NO and UK orders each going to their own later steps, or the high-value orders taking a different path from the rest. There are eight routes plus Everything else; if you need more, feed Everything else into a second Route rows step.\n\nBecause the first match wins, order the rules from most specific to least. A rule for \"score is 90 or more\" has to come BEFORE \"score is 50 or more\" — the other way round, the looser rule swallows the rows the strict one was meant to catch.",
			Summary:     "Route each row to one of N output slots based on the first matching CEL filter; rest go to default.",
			Examples: []core.ParamsExample{
				{
					Title:  "Three-way split by country",
					Params: json.RawMessage(`{"routes":[{"slot":"rows_1","filter":"row.country == 'SE'"},{"slot":"rows_2","filter":"row.country == 'NO'"},{"slot":"rows_3","filter":"row.country == 'DK'"}]}`),
					Notes:  "Anything that isn't SE/NO/DK lands on the 'default' output port.",
				},
				{
					Title:  "Priority routing by score",
					Params: json.RawMessage(`{"routes":[{"slot":"rows_1","filter":"row.score >= 90"},{"slot":"rows_2","filter":"row.score >= 50"}]}`),
					Notes:  "First-match-wins: a row with score 95 goes to rows_1 even though it also satisfies the rows_2 filter.",
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "rows", Label: "Rows", Required: true, MIME: []string{"application/json"}},
			},
			Outputs: outputs,
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"routes": {
						"type":"array",
						"title":"Rules",
						"description":"The rules, in order. Each row takes the FIRST rule it satisfies and leaves on that rule's route; a row satisfying none leaves on Everything else. Order matters — put the most specific rule first, or a looser one above it will swallow the rows it was meant to catch.",
						"items": {
							"type":"object",
							"properties":{
								"slot":   {"type":"string","title":"Route to send it down","description":"Which output the matching rows leave on. \"rows_1\" is the pin labelled Route 1, \"rows_2\" is Route 2, and so on up to \"rows_8\"."},
								"filter": {"type":"string","format":"row-condition","description":"The condition a row has to satisfy to take this route — row.country == 'SE', say, or row.total > 1000. Written as a CEL expression, in which 'row' is the row and row.<column name> is one of its columns."}
							},
							"required":["slot","filter"]
						}
					},
					"default_slot": {"type":"string","title":"Where the leftovers go","default":"default","description":"Which output catches the rows that satisfy no rule at all. \"default\" is the pin labelled Everything else; name one of rows_1..rows_8 here instead to fold the leftovers in with a route you are already using."}
				},
				"required":["routes"]
			}`),
			Idempotent: true,
		},
		Execute: executeRouteRows,
	})
}

type routeSpec struct {
	slot   string
	filter string
}

func executeRouteRows(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	specs, defaultSlot, err := parseRouteParams(job.Params)
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}

	rows, headers, errRes, ok := loadRowsAndHeaders(job)
	if !ok {
		return errRes, nil
	}

	env, err := newRowCELEnv()
	if err != nil {
		return params.Err(job, "internal", fmt.Sprintf("cel env: %v", err)), nil
	}
	progs := make([]cel.Program, len(specs))
	for i, s := range specs {
		ast, issues := env.Compile(s.filter)
		if issues != nil && issues.Err() != nil {
			return params.Err(job, "bad_param",
				fmt.Sprintf("routes[%d] (slot %q): compile filter: %v", i, s.slot, issues.Err())), nil
		}
		prog, err := celProgram(env, ast)
		if err != nil {
			return params.Err(job, "internal",
				fmt.Sprintf("routes[%d] (slot %q): build program: %v", i, s.slot, err)), nil
		}
		progs[i] = prog
	}

	bucket := make(map[string][]map[string]any, len(specs)+1)
	for _, s := range specs {
		if _, ok := bucket[s.slot]; !ok {
			bucket[s.slot] = make([]map[string]any, 0)
		}
	}
	bucket[defaultSlot] = make([]map[string]any, 0)

	for i, row := range rows {
		routed := false
		for j, spec := range specs {
			pass, err := evalFilter(ctx, progs[j], row)
			if err != nil {
				return params.Err(job, "eval",
					fmt.Sprintf("row %d, routes[%d] (slot %q): %v", i, j, spec.slot, err)), nil
			}
			if pass {
				bucket[spec.slot] = append(bucket[spec.slot], row)
				routed = true
				break // first-match-wins
			}
		}
		if !routed {
			bucket[defaultSlot] = append(bucket[defaultSlot], row)
		}
	}

	out := map[string]core.Ref{}
	for slot, rows := range bucket {
		out[slot] = core.Ref{MIME: "application/json", Inline: rows, Headers: headers}
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: out,
	}, nil
}

// parseRouteParams pulls (routes, default_slot) off Job.Params with
// validation. Every route's slot must be one of the manifest's
// declared rows_N ports (or the default slot) — otherwise a typo
// silently drops rows into a nonexistent port.
func parseRouteParams(params map[string]any) ([]routeSpec, string, error) {
	raw, ok := params["routes"]
	if !ok {
		return nil, "", fmt.Errorf("routes: required (ordered list of {slot, filter})")
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil, "", fmt.Errorf("routes: expected array, got %T", raw)
	}
	if len(arr) == 0 {
		return nil, "", fmt.Errorf("routes: at least one route required (otherwise everything routes to default — use a simpler step)")
	}
	defaultSlot := routeDefaultSlot
	if v, ok := params["default_slot"]; ok {
		s, ok := v.(string)
		if !ok {
			return nil, "", fmt.Errorf("default_slot: expected string, got %T", v)
		}
		if s != "" {
			defaultSlot = s
		}
	}
	validSlots := validSlotSet()
	if _, ok := validSlots[defaultSlot]; !ok {
		return nil, "", fmt.Errorf("default_slot %q is not a known output port (use one of rows_1..rows_%d or %q)", defaultSlot, routeSlotCount, routeDefaultSlot)
	}

	specs := make([]routeSpec, 0, len(arr))
	for i, item := range arr {
		obj, ok := item.(map[string]any)
		if !ok {
			return nil, "", fmt.Errorf("routes[%d]: expected object, got %T", i, item)
		}
		slot, _ := obj["slot"].(string)
		if slot == "" {
			return nil, "", fmt.Errorf("routes[%d]: missing or empty 'slot'", i)
		}
		if _, ok := validSlots[slot]; !ok {
			return nil, "", fmt.Errorf("routes[%d]: slot %q is not a known output port (use one of rows_1..rows_%d)", i, slot, routeSlotCount)
		}
		if slot == defaultSlot {
			return nil, "", fmt.Errorf("routes[%d]: slot %q is also the default_slot — explicit routes mustn't collide with the catch-all", i, slot)
		}
		filter, _ := obj["filter"].(string)
		if filter == "" {
			return nil, "", fmt.Errorf("routes[%d] (slot %q): missing or empty 'filter'", i, slot)
		}
		specs = append(specs, routeSpec{slot: slot, filter: filter})
	}
	return specs, defaultSlot, nil
}

// validSlotSet returns the set of legal output port names (rows_1..N + default).
// Built once per call; the set is small enough that caching globally
// would only save a few hundred bytes of work.
func validSlotSet() map[string]struct{} {
	out := make(map[string]struct{}, routeSlotCount+1)
	for i := 1; i <= routeSlotCount; i++ {
		out[fmt.Sprintf("rows_%d", i)] = struct{}{}
	}
	out[routeDefaultSlot] = struct{}{}
	return out
}
