// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package transform

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/cel-go/cel"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/engine"
)

// routeSlotCount is how many named routing outputs the manifest
// declares. Enough for most real-world routing without making the
// node visually crowded; routes beyond this either fold into
// `default` or compose two route_rows.
//
// Each slot is `rows_<N>` where N is 1..routeSlotCount. The slot name
// is what the user references from params; downstream nodes can label
// the semantic meaning (e.g. wire `rows_1` to "SE pipeline").
//
// **Why fixed slots for V1:** true variadic-by-name outputs
// (`{SE: ..., NO: ...}`) need editor support to render per-name
// handles — that's the open Editor → "Variadic input/output ports"
// follow-up. Fixed slots ship a usable N-way split TODAY against the
// current editor; the upgrade path is purely additive (semantic
// names) without behavioral changes.
const routeSlotCount = 8

// routeDefaultSlot is the catch-all port name for rows that don't
// match any explicit route. Always present in the manifest; users
// can override which slot acts as default via params.default_slot.
const routeDefaultSlot = "default"

func init() {
	outputs := make([]core.Port, 0, routeSlotCount+2)
	for i := 1; i <= routeSlotCount; i++ {
		outputs = append(outputs, core.Port{
			Port:  fmt.Sprintf("rows_%d", i),
			Label: fmt.Sprintf("Routing slot %d", i),
			MIME:  []string{"application/json"},
			List:  true, // carries rows — name isn't in the auto-list set, so flag it
		})
	}
	outputs = append(outputs,
		core.Port{Port: routeDefaultSlot, Label: "Default", MIME: []string{"application/json"}, List: true},
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
			Description: "N-way split. Param `routes` is an ordered list of {slot, filter} entries — for each row, the FIRST filter that returns true wins and the row goes to that slot's output port. Rows matching no route land on `default`. Use this to fan one row stream into per-category pipelines (e.g. route SE/NO/UK orders to different later steps). Output slot names are fixed (rows_1..rows_8 + default) for V1; semantic naming via variadic ports is a future enhancement.",
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
						"description":"Ordered list of routing rules. Each row is sent to the FIRST matching route; unmatched rows land on the default slot.",
						"items": {
							"type":"object",
							"properties":{
								"slot":   {"type":"string","description":"Output port name. One of rows_1..rows_8."},
								"filter": {"type":"string","format":"row-condition","description":"CEL expression returning bool. Sees 'row' as map<string,dyn>."}
							},
							"required":["slot","filter"]
						}
					},
					"default_slot": {"type":"string","default":"default","description":"Slot name that catches rows matching no route. Defaults to 'default'."}
				},
				"required":["routes"]
			}`),
			Idempotent: true,
		},
		Execute: executeRouteRows,
	})
}

// routeSpec is one parsed routing rule. Compiled CEL programs go in
// progs (one per spec, matched by index) so the row loop doesn't
// recompile per iteration.
type routeSpec struct {
	slot   string
	filter string
}

func executeRouteRows(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	specs, defaultSlot, err := parseRouteParams(job.Params)
	if err != nil {
		return errResult(job, "bad_param", err.Error()), nil
	}

	rows, headers, errRes, ok := loadRowsAndHeaders(job)
	if !ok {
		return errRes, nil
	}

	// Compile CEL filters once, before the row loop. Bad expressions
	// fail the whole batch up front (same contract as compute_rows /
	// split_rows) — partial routing is worse than none for
	// downstream consumers expecting deterministic slot sizes.
	env, err := newRowCELEnv()
	if err != nil {
		return errResult(job, "internal", fmt.Sprintf("cel env: %v", err)), nil
	}
	progs := make([]cel.Program, len(specs))
	for i, s := range specs {
		ast, issues := env.Compile(s.filter)
		if issues != nil && issues.Err() != nil {
			return errResult(job, "bad_param",
				fmt.Sprintf("routes[%d] (slot %q): compile filter: %v", i, s.slot, issues.Err())), nil
		}
		prog, err := celProgram(env, ast)
		if err != nil {
			return errResult(job, "internal",
				fmt.Sprintf("routes[%d] (slot %q): build program: %v", i, s.slot, err)), nil
		}
		progs[i] = prog
	}

	// Allocate per-slot buckets — only for slots referenced in
	// params plus the default. Unused manifest slots emit no rows
	// (the engine treats no-output as a dormant edge, so downstream
	// of an unused slot correctly skips).
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
				return errResult(job, "eval",
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
