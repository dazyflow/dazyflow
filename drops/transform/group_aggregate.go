// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package transform

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/engine"
)

// Aggregation operations. Mirrors the SQL set most users reach for —
// count/sum/avg for numerics, min/max for orderable values,
// first/last for "pick a representative", collect for fanout to lists.
const (
	aggCount   = "count"
	aggSum     = "sum"
	aggAvg     = "avg"
	aggMin     = "min"
	aggMax     = "max"
	aggFirst   = "first"
	aggLast    = "last"
	aggCollect = "collect"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "group_aggregate",
			Version:     "1.0",
			Label:       "Group & summarize",
			Icon:        "square-stack",
			Category:    "transformation",
			Provider:    "internal",
			Tags:        []string{"transform", "group", "aggregate", "pivot", "sum", "etl", "sql"},
			Description: "Group rows by N columns and emit one row per group with aggregated values. Param `by` is the list of grouping columns. Param `aggregate` maps each output column to {op, column} — supported ops: count (no column needed), sum, avg, min, max, first, last, collect (gathers values into a list). Numeric ops coerce strings (\"30\") and ints/floats so Excel/JSON mixed inputs work without pre-casting. min/max falls back to lexical comparison when values aren't all numeric. Groups are emitted in first-seen order. by:[] = single group covering all rows — useful for whole-input totals.",
			Summary:     "Group rows by N columns and emit one aggregated row per group (count/sum/avg/min/max/first/last/collect).",
			Examples: []core.ParamsExample{
				{
					Title:  "Orders per country, with revenue totals",
					Params: json.RawMessage(`{"by":["country"],"aggregate":{"order_count":{"op":"count"},"revenue":{"op":"sum","column":"amount"},"avg_ticket":{"op":"avg","column":"amount"}}}`),
				},
				{
					Title:  "Whole-input totals (by:[] = one group)",
					Params: json.RawMessage(`{"by":[],"aggregate":{"total_rows":{"op":"count"},"total":{"op":"sum","column":"amount"}}}`),
				},
				{
					Title:  "Collect SKUs per customer",
					Params: json.RawMessage(`{"by":["customer_id"],"aggregate":{"skus":{"op":"collect","column":"sku"},"first_seen":{"op":"min","column":"created_at"}}}`),
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "rows", Label: "Rows", Required: true, MIME: []string{"application/json"}},
			},
			Outputs: []core.Port{
				{Port: "rows", Label: "Rows", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"by":        {"type":"array","items":{"type":"string"},"description":"Columns to group by. Empty list = a single group covering all input rows."},
					"aggregate": {
						"type":"object",
						"description":"Map of output_column_name → {op, column?}. column is required for sum/avg/min/max/first/last/collect; omitted for count. The short form {\"revenue\":\"sum\"} is also accepted: the op alone, with the output name doubling as the source column.",
						"additionalProperties": {
							"type":"object",
							"properties":{
								"op":     {"type":"string","enum":["count","sum","avg","min","max","first","last","collect"]},
								"column": {"type":"string"}
							},
							"required":["op"]
						}
					}
				},
				"required":["by","aggregate"]
			}`),
			Idempotent: true,
		},
		Execute: executeGroupAggregate,
	})
}

// aggSpec is the parsed-from-params shape of one aggregation
// instruction. Output names come from the params map's keys.
type aggSpec struct {
	output string // the column name in the result
	op     string // count / sum / avg / ...
	column string // source column ("" for count)
}

// executeGroupAggregate walks input rows once, accumulates per-group
// state, then emits one row per group. Time: O(R * A) where A is the
// number of aggregations. Memory: O(G * A) for the accumulators (G =
// distinct groups) plus O(R) for the rows in `collect` ops.
func executeGroupAggregate(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	by, aggs, err := parseGroupParams(job.Params)
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}

	rows, headers, errRes, ok := loadRowsAndHeaders(job)
	if !ok {
		return errRes, nil
	}

	// Verify columns referenced by params actually exist in the
	// declared/derived headers. Catches typos up-front instead of
	// silently producing zeros.
	have := make(map[string]struct{}, len(headers))
	for _, h := range headers {
		have[h] = struct{}{}
	}
	if len(rows) > 0 || len(headers) > 0 {
		for _, col := range by {
			if _, ok := have[col]; !ok {
				return params.Err(job, "bad_input",
					fmt.Sprintf("by: column %q not in input headers (%v)", col, headers)), nil
			}
		}
		for _, a := range aggs {
			if a.op == aggCount {
				continue
			}
			if _, ok := have[a.column]; !ok {
				return params.Err(job, "bad_input",
					fmt.Sprintf("aggregate %q: column %q not in input headers (%v)", a.output, a.column, headers)), nil
			}
		}
	}

	// groupAcc tracks one group's running aggregation state. count
	// is incremented for every row regardless of aggregations so
	// `avg = sum / count` works without a separate counter per agg.
	type groupAcc struct {
		keyValues map[string]any         // by columns of the first row in this group
		ops       map[string]*aggOpState // keyed by output name
	}
	groups := make(map[string]*groupAcc)
	order := make([]string, 0) // first-seen group order, for deterministic output

	for i, row := range rows {
		key := keyString(row, by)
		acc, ok := groups[key]
		if !ok {
			kv := make(map[string]any, len(by))
			for _, c := range by {
				kv[c] = row[c]
			}
			acc = &groupAcc{
				keyValues: kv,
				ops:       make(map[string]*aggOpState, len(aggs)),
			}
			for _, a := range aggs {
				acc.ops[a.output] = &aggOpState{spec: a}
			}
			groups[key] = acc
			order = append(order, key)
		}
		for _, a := range aggs {
			if err := acc.ops[a.output].observe(row); err != nil {
				return params.Err(job, "eval",
					fmt.Sprintf("aggregate %q on row %d: %v", a.output, i, err)), nil
			}
		}
	}

	// Materialise output rows in first-seen group order, then
	// finalize each accumulator (avg divides, collect snapshots).
	out := make([]map[string]any, 0, len(order))
	for _, key := range order {
		acc := groups[key]
		r := make(map[string]any, len(by)+len(aggs))
		for c, v := range acc.keyValues {
			r[c] = v
		}
		for _, a := range aggs {
			r[a.output] = acc.ops[a.output].finalize()
		}
		out = append(out, r)
	}

	// Output headers: by-columns in input order, then aggregation
	// outputs alphabetized for stability (map iteration during
	// parsing isn't deterministic).
	outHeaders := append([]string(nil), by...)
	aggNames := make([]string, 0, len(aggs))
	for _, a := range aggs {
		aggNames = append(aggNames, a.output)
	}
	sort.Strings(aggNames)
	outHeaders = append(outHeaders, aggNames...)

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"rows": {MIME: "application/json", Inline: out, Headers: outHeaders},
		},
	}, nil
}

// parseGroupParams pulls (by, aggregations) off Job.Params with
// validation. Aggregations come out sorted by output name for
// deterministic per-row order in the accumulator pass.
func parseGroupParams(params map[string]any) ([]string, []aggSpec, error) {
	byRaw, ok := params["by"]
	if !ok {
		return nil, nil, fmt.Errorf("by: required (list of group columns; [] for a single total group)")
	}
	by, err := normalizeStringSlice(byRaw, "by")
	if err != nil {
		return nil, nil, err
	}
	aggsRaw, ok := params["aggregate"]
	if !ok {
		return nil, nil, fmt.Errorf("aggregate: required (map of output_column → {op, column})")
	}
	aggsMap, ok := aggsRaw.(map[string]any)
	if !ok {
		return nil, nil, fmt.Errorf("aggregate: expected object, got %T", aggsRaw)
	}
	if len(aggsMap) == 0 {
		return nil, nil, fmt.Errorf("aggregate: at least one aggregation required")
	}
	aggs := make([]aggSpec, 0, len(aggsMap))
	for outName, raw := range aggsMap {
		// Short form: {"revenue": "sum"} — the op alone, with the output name
		// doubling as the source column ({"orders": "count"} needs no column).
		// It's what people write by hand, and the long form's nested objects
		// are a stumble the editor's form hides but hand-authored graphs don't.
		if op, isShort := raw.(string); isShort {
			raw = map[string]any{"op": op, "column": outName}
			if op == aggCount {
				raw = map[string]any{"op": op}
			}
		}
		spec, ok := raw.(map[string]any)
		if !ok {
			return nil, nil, fmt.Errorf("aggregate %q: expected object or op name, got %T", outName, raw)
		}
		opRaw, ok := spec["op"]
		if !ok {
			return nil, nil, fmt.Errorf("aggregate %q: missing 'op'", outName)
		}
		op, ok := opRaw.(string)
		if !ok {
			return nil, nil, fmt.Errorf("aggregate %q: op must be a string, got %T", outName, opRaw)
		}
		switch op {
		case aggCount, aggSum, aggAvg, aggMin, aggMax, aggFirst, aggLast, aggCollect:
		default:
			return nil, nil, fmt.Errorf("aggregate %q: unknown op %q (want count|sum|avg|min|max|first|last|collect)", outName, op)
		}
		col, _ := spec["column"].(string)
		if op != aggCount && col == "" {
			return nil, nil, fmt.Errorf("aggregate %q: op %q requires 'column'", outName, op)
		}
		aggs = append(aggs, aggSpec{output: outName, op: op, column: col})
	}
	// Sort by output name so iteration is deterministic — affects
	// only the order observe() is called per row, which the
	// accumulators don't depend on, but keeps the pass cache-
	// friendly and reproducible.
	sort.Slice(aggs, func(i, j int) bool { return aggs[i].output < aggs[j].output })
	return by, aggs, nil
}

// aggOpState holds the running computation for ONE aggregation in ONE
// group. All accumulators are folded into a single struct so the
// observe()/finalize() dispatch stays in one switch — fewer types,
// fewer allocations.
type aggOpState struct {
	spec aggSpec

	// Numeric accumulators (sum / avg / min / max numeric path).
	numericValid bool // true once at least one numeric value landed
	sumFloat     float64
	count        int

	// Generic min/max — kept alongside numeric so we can fall back
	// to lexical comparison if non-numeric values appear.
	minAny any
	maxAny any
	hasAny bool

	// first / last / collect carry raw values.
	first    any
	hasFirst bool
	last     any
	collect  []any
}

// observe folds one row's contribution into the accumulator. Errors
// surface for genuinely bad data (non-coercible strings on sum/avg);
// "missing column" produces a nil treated as zero / skipped per op.
func (s *aggOpState) observe(row map[string]any) error {
	switch s.spec.op {
	case aggCount:
		s.count++
	case aggSum, aggAvg:
		raw, ok := row[s.spec.column]
		if !ok || raw == nil {
			// Skip nil values (SQL convention — nulls don't
			// contribute to sum, avg). count still increments only
			// when we successfully add a number; otherwise avg
			// would divide by the wrong denominator.
			return nil
		}
		n, err := coerceNumeric(raw)
		if err != nil {
			return fmt.Errorf("column %q: %w", s.spec.column, err)
		}
		s.sumFloat += n
		s.count++
		s.numericValid = true
	case aggMin, aggMax:
		raw, ok := row[s.spec.column]
		if !ok || raw == nil {
			return nil
		}
		if n, err := coerceNumeric(raw); err == nil {
			// Numeric path — fast and well-ordered.
			if !s.numericValid {
				s.sumFloat = n // reuse sumFloat as the running extremum
				s.numericValid = true
			} else if (s.spec.op == aggMin && n < s.sumFloat) || (s.spec.op == aggMax && n > s.sumFloat) {
				s.sumFloat = n
			}
		} else {
			// Non-numeric — lexical comparison via fmt.Sprint.
			str := fmt.Sprint(raw)
			if !s.hasAny {
				s.minAny, s.maxAny, s.hasAny = raw, raw, true
			} else {
				curStr := fmt.Sprint(s.minAny)
				if s.spec.op == aggMin && str < curStr {
					s.minAny = raw
				}
				curStr = fmt.Sprint(s.maxAny)
				if s.spec.op == aggMax && str > curStr {
					s.maxAny = raw
				}
			}
		}
	case aggFirst:
		if !s.hasFirst {
			raw := row[s.spec.column]
			s.first = raw
			s.hasFirst = true
		}
	case aggLast:
		s.last = row[s.spec.column]
		s.hasFirst = true // reused as "has any value seen"
	case aggCollect:
		s.collect = append(s.collect, row[s.spec.column])
	}
	return nil
}

// finalize converts the accumulator into the output value for this
// group. Called once per group, after every row has been observed.
func (s *aggOpState) finalize() any {
	switch s.spec.op {
	case aggCount:
		return s.count
	case aggSum:
		if !s.numericValid {
			return 0
		}
		// Prefer int output when the sum is integral — avoids
		// "30" turning into "30.0" downstream for tidy display.
		if s.sumFloat == float64(int64(s.sumFloat)) {
			return int64(s.sumFloat)
		}
		return s.sumFloat
	case aggAvg:
		if s.count == 0 {
			return nil
		}
		return s.sumFloat / float64(s.count)
	case aggMin, aggMax:
		if s.numericValid {
			if s.sumFloat == float64(int64(s.sumFloat)) {
				return int64(s.sumFloat)
			}
			return s.sumFloat
		}
		if !s.hasAny {
			return nil
		}
		if s.spec.op == aggMin {
			return s.minAny
		}
		return s.maxAny
	case aggFirst:
		if !s.hasFirst {
			return nil
		}
		return s.first
	case aggLast:
		if !s.hasFirst {
			return nil
		}
		return s.last
	case aggCollect:
		// Always return a non-nil slice so downstream consumers
		// don't have to special-case "empty group" vs "no list".
		if s.collect == nil {
			return []any{}
		}
		return s.collect
	}
	return nil
}

// coerceNumeric coerces a row value into float64. Accepts int
// variants, float, json.Number, and string ("30", "30.5"). Returns a
// clear error for anything else — sum on a map[string]any is a graph
// bug, not a silent zero. Distinct from sort_rows.toFloat (which
// returns a bool ok) because aggregations want to surface the bad
// value to the user as a per-row error, not silently downgrade to
// lexical comparison the way sort does.
func coerceNumeric(v any) (float64, error) {
	switch n := v.(type) {
	case float64:
		return n, nil
	case float32:
		return float64(n), nil
	case int:
		return float64(n), nil
	case int32:
		return float64(n), nil
	case int64:
		return float64(n), nil
	case json.Number:
		return n.Float64()
	case string:
		s := strings.TrimSpace(n)
		if s == "" {
			return 0, fmt.Errorf("empty string is not numeric")
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0, fmt.Errorf("not numeric: %q", s)
		}
		return f, nil
	case bool:
		// Booleans aren't numeric — coercing true→1 would let
		// users sum a checkbox column accidentally, which is
		// confusing. Better to fail loud.
		return 0, fmt.Errorf("bool is not numeric")
	}
	return 0, fmt.Errorf("type %T is not numeric", v)
}
