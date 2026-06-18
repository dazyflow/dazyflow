package transform

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types/ref"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "compute_rows",
			Version:     "1.0",
			Label:       "Compute rows",
			Icon:        "cpu",
			Category:    "transformation",
			Provider:    "internal",
			Tags:        []string{"transform", "compute", "expression", "cel", "derived", "filter", "etl"},
			Description: "Add derived columns and filter rows using CEL (Google's Common Expression Language) expressions. Each expression sees the row as the variable `row` (a map of string→dyn): `row.first_name + ' ' + row.last_name`, `row.age >= 18`, `row.score > 90 ? 'gold' : 'bronze'`. Compute adds or overwrites columns; filter drops rows whose expression evaluates to false. The expressive sibling of map_rows — reach for it only when static config can't say what you mean.",
			Summary:     "Add derived columns and drop rows using CEL expressions evaluated against each row.",
			Examples: []core.ParamsExample{
				{
					Title:  "Add a full_name column",
					Params: json.RawMessage(`{"compute":{"full_name":"row.first_name + ' ' + row.last_name"}}`),
				},
				{
					Title:  "Tier rows and filter adults only",
					Params: json.RawMessage(`{"filter":"row.age >= 18","compute":{"tier":"row.score > 90 ? 'gold' : (row.score > 50 ? 'silver' : 'bronze')"}}`),
					Notes:  "filter runs before compute, so the filter expression sees only input columns.",
				},
				{
					Title:  "Filter-only (no derived columns)",
					Params: json.RawMessage(`{"filter":"row.status == 'active' && row.country == 'SE'"}`),
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "rows", Label: "Rows", Required: true, MIME: []string{"application/json"}},
				{Port: "headers", Label: "Headers", Required: false, MIME: []string{"application/json"}},
			},
			Outputs: []core.Port{
				{Port: "rows", Label: "Rows", MIME: []string{"application/json"}},
				{Port: "headers", Label: "Headers", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"compute": {
						"type":"object",
						"additionalProperties":{"type":"string"},
						"description":"Map of {output_column: CEL expression}. The expression's value becomes that cell. Existing columns are overwritten; new columns are added to the output schema."
					},
					"filter": {
						"type":"string",
						"format":"row-condition",
						"description":"CEL expression that must evaluate to a bool. Rows where it returns false are dropped."
					}
				}
			}`),
			Idempotent: true,
		},
		Execute: executeComputeRows,
	})
}

// executeComputeRows evaluates CEL expressions over each row,
// adding derived columns and dropping rows that fail a filter. The
// design is the deliberate companion to map_rows: same input/output
// shape, same headers handling, same all-or-nothing batch contract,
// but a real expression language for the cases map_rows can't reach
// (string concat, arithmetic, multi-column predicates, conditionals).
//
// Expressions are compiled once before the row loop. Per-row failure
// (type mismatch, undefined field) fails the whole job rather than
// silently dropping the row — like the SQL insert drops, partial
// completion is worse than no completion for ETL.
//
// Order of operations: filter first, then compute. This lets a
// filter expression refer ONLY to existing input columns; computed
// columns aren't visible to filter. Users wanting "filter on a
// computed value" should chain compute_rows → map_rows.
func executeComputeRows(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	rowsRef, ok := job.Input["rows"]
	if !ok {
		return errResult(job, "missing_input", "input port 'rows' is required"), nil
	}
	rows, err := normalizeRows(rowsRef.Inline)
	if err != nil {
		return errResult(job, "bad_input", err.Error()), nil
	}

	var inputHeaders []string
	if h, ok := job.Input["headers"]; ok && h.Inline != nil {
		inputHeaders, err = normalizeHeaders(h.Inline)
		if err != nil {
			return errResult(job, "bad_input", err.Error()), nil
		}
	}
	if inputHeaders == nil {
		inputHeaders = deriveHeaders(rows)
	}

	env, err := newRowCELEnv()
	if err != nil {
		return errResult(job, "internal", fmt.Sprintf("cel env: %v", err)), nil
	}

	filterProg, err := compileOptionalFilter(env, job.Params)
	if err != nil {
		return errResult(job, "bad_param", err.Error()), nil
	}

	steps, err := compileComputeSteps(env, job.Params)
	if err != nil {
		return errResult(job, "bad_param", err.Error()), nil
	}

	// Output headers = input headers + any compute keys that aren't
	// already columns. Preserve compute insertion order so the output
	// schema is predictable across runs.
	outputHeaders := append([]string{}, inputHeaders...)
	seen := make(map[string]struct{}, len(inputHeaders))
	for _, h := range inputHeaders {
		seen[h] = struct{}{}
	}
	for _, s := range steps {
		if _, ok := seen[s.column]; !ok {
			outputHeaders = append(outputHeaders, s.column)
			seen[s.column] = struct{}{}
		}
	}

	out := make([]map[string]any, 0, len(rows))
	for i, row := range rows {
		if filterProg != nil {
			pass, err := evalFilter(ctx, filterProg, row)
			if err != nil {
				return errResult(job, "eval", fmt.Sprintf("filter row %d: %v", i, err)), nil
			}
			if !pass {
				continue
			}
		}

		// Clone before computing so we don't mutate the input row
		// (which may be shared with whatever upstream node emitted
		// it). The clone is shallow because CEL eval reads but never
		// writes nested structures.
		newRow := make(map[string]any, len(row)+len(steps))
		for k, v := range row {
			newRow[k] = v
		}
		for _, s := range steps {
			v, err := evalExpression(ctx, s.prog, newRow)
			if err != nil {
				return errResult(job, "eval",
					fmt.Sprintf("compute[%q] row %d: %v", s.column, i, err)), nil
			}
			newRow[s.column] = v
		}
		out = append(out, newRow)
	}

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"rows":    {MIME: "application/json", Inline: out},
			"headers": {MIME: "application/json", Inline: outputHeaders},
		},
	}, nil
}

type computeStep struct {
	column string
	prog   cel.Program
}

// compileComputeSteps builds one cel.Program per (column, expression)
// pair, in sorted-key order. Sorting matters because Go map
// iteration is randomized and two compute keys could each reference
// the other in their expressions — without a stable order, behavior
// would flake test-to-test. Alphabetical isn't a perfect
// dependency order, but it's deterministic and documented.
func compileComputeSteps(env *cel.Env, params map[string]any) ([]computeStep, error) {
	raw, ok := params["compute"]
	if !ok || raw == nil {
		return nil, nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("compute: expected object, got %T", raw)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	steps := make([]computeStep, 0, len(keys))
	for _, k := range keys {
		exprStr, ok := m[k].(string)
		if !ok {
			return nil, fmt.Errorf("compute[%q]: expected string expression, got %T", k, m[k])
		}
		ast, issues := env.Compile(exprStr)
		if issues != nil && issues.Err() != nil {
			return nil, fmt.Errorf("compute[%q]: %v", k, issues.Err())
		}
		prog, err := celProgram(env, ast)
		if err != nil {
			return nil, fmt.Errorf("compute[%q]: program: %w", k, err)
		}
		steps = append(steps, computeStep{column: k, prog: prog})
	}
	return steps, nil
}

func compileOptionalFilter(env *cel.Env, params map[string]any) (cel.Program, error) {
	raw, ok := params["filter"]
	if !ok || raw == nil {
		return nil, nil
	}
	exprStr, ok := raw.(string)
	if !ok {
		return nil, fmt.Errorf("filter: expected string, got %T", raw)
	}
	if exprStr == "" {
		return nil, nil
	}
	ast, issues := env.Compile(exprStr)
	if issues != nil && issues.Err() != nil {
		return nil, fmt.Errorf("filter: %v", issues.Err())
	}
	return celProgram(env, ast)
}

func evalFilter(_ context.Context, prog cel.Program, row map[string]any) (bool, error) {
	v, _, err := prog.Eval(celVars(row))
	if err != nil {
		return false, err
	}
	b, ok := v.Value().(bool)
	if !ok {
		return false, fmt.Errorf("filter expression must return bool, got %T", v.Value())
	}
	return b, nil
}

func evalExpression(_ context.Context, prog cel.Program, row map[string]any) (any, error) {
	v, _, err := prog.Eval(celVars(row))
	if err != nil {
		return nil, err
	}
	return unwrapCEL(v)
}

// unwrapCEL converts a CEL ref.Val back to a plain Go value suitable
// for downstream JSON marshaling. Primitives come out as their
// natural Go type (int64, float64, bool, string); composite types
// (list, map) get unwrapped recursively via ConvertToNative so a
// computed `{"a": 1}` doesn't surface as a CEL types.Map wrapper.
func unwrapCEL(v ref.Val) (any, error) {
	raw := v.Value()
	switch raw.(type) {
	case nil, bool, string, int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64, float32, float64:
		return raw, nil
	}
	// Composite types: use ConvertToNative with the any-interface
	// target so cel-go does the recursive unwrap for us.
	native, err := v.ConvertToNative(reflect.TypeOf((*any)(nil)).Elem())
	if err != nil {
		return raw, nil // fall back to the wrapped value; better than dropping
	}
	return native, nil
}
