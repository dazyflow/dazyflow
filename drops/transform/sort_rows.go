package transform

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"

	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:             "sort_rows",
			Version:        "1.0",
			Label:          "Sort rows",
			Color:          "#9c6dff",
			Icon:           "cpu",
			Category:       "transformation",
			Provider:       "internal",
			Tags:           []string{"transform", "sort", "order", "etl"},
			Description:    "Sort rows by one or more columns. The 'by' param accepts a list of column names (ascending) or {column, desc:true} objects for descending. Multi-column sorts are stable in the order listed — earlier keys win, later keys break ties.",
			Summary:        "Stably sort rows by one or more columns, ascending or descending per key.",
			Examples: []core.ParamsExample{
				{
					Title:  "Sort by created_at ascending",
					Params: json.RawMessage(`{"by":["created_at"]}`),
				},
				{
					Title:  "Highest revenue first, then alphabetical name",
					Params: json.RawMessage(`{"by":[{"column":"revenue","desc":true},"name"]}`),
					Notes:  "Multi-key sort is stable: rows with equal revenue keep their name-order tiebreak.",
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
					"by":{
						"type":"array",
						"items":{
							"oneOf":[
								{"type":"string"},
								{"type":"object","properties":{"column":{"type":"string"},"desc":{"type":"boolean"}},"required":["column"]}
							]
						},
						"description":"Sort keys in priority order. A bare string means ascending; {column,desc:true} flips that column to descending."
					}
				},
				"required":["by"]
			}`),
			Idempotent: true,
		},
		Execute: executeSortRows,
	})
}

type sortKey struct {
	column string
	desc   bool
}

// executeSortRows orders rows by the listed keys, stably. Headers
// pass through unchanged — sort is row-order-only, not schema-
// changing. Like map_rows, the input row map is never mutated; we
// sort a copied slice and emit the new ordering.
func executeSortRows(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	rowsRef, ok := job.Input["rows"]
	if !ok {
		return errResult(job, "missing_input", "input port 'rows' is required"), nil
	}
	rows, err := normalizeRows(rowsRef.Inline)
	if err != nil {
		return errResult(job, "bad_input", err.Error()), nil
	}

	var headers []string
	if h, ok := job.Input["headers"]; ok && h.Inline != nil {
		headers, err = normalizeHeaders(h.Inline)
		if err != nil {
			return errResult(job, "bad_input", err.Error()), nil
		}
	}
	if headers == nil {
		headers = deriveHeaders(rows)
	}

	keys, err := parseSortKeys(job.Params)
	if err != nil {
		return errResult(job, "bad_param", err.Error()), nil
	}

	// Sort a copy so the input slice (potentially shared with the
	// upstream node's output) stays in its original order.
	out := make([]map[string]any, len(rows))
	copy(out, rows)
	sort.SliceStable(out, func(i, j int) bool {
		for _, k := range keys {
			cmp := compareCells(out[i][k.column], out[j][k.column])
			if cmp == 0 {
				continue
			}
			if k.desc {
				return cmp > 0
			}
			return cmp < 0
		}
		return false
	})

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"rows":    {MIME: "application/json", Inline: out},
			"headers": {MIME: "application/json", Inline: headers},
		},
	}, nil
}

// parseSortKeys accepts the loose JSON shape the schema documents:
// bare strings ("name" → ascending) and objects ({column,desc}). One
// pass through the list, no auto-detect of column types — that
// happens lazily inside the comparator.
func parseSortKeys(params map[string]any) ([]sortKey, error) {
	raw, ok := params["by"]
	if !ok || raw == nil {
		return nil, fmt.Errorf("by: required")
	}
	arr, ok := raw.([]any)
	if !ok {
		// Native callers (tests) may pass []string or []sortKey-like
		// shapes; accept the common ones.
		if ss, ok := raw.([]string); ok {
			out := make([]sortKey, len(ss))
			for i, s := range ss {
				out[i] = sortKey{column: s}
			}
			return out, nil
		}
		return nil, fmt.Errorf("by: expected array, got %T", raw)
	}
	if len(arr) == 0 {
		return nil, fmt.Errorf("by: must list at least one key")
	}
	keys := make([]sortKey, 0, len(arr))
	for i, item := range arr {
		switch v := item.(type) {
		case string:
			keys = append(keys, sortKey{column: v})
		case map[string]any:
			col, _ := v["column"].(string)
			if col == "" {
				return nil, fmt.Errorf("by[%d]: 'column' missing", i)
			}
			desc, _ := v["desc"].(bool)
			keys = append(keys, sortKey{column: col, desc: desc})
		default:
			return nil, fmt.Errorf("by[%d]: expected string or {column,desc}, got %T", i, item)
		}
	}
	return keys, nil
}

// compareCells orders two cell values: -1 if a < b, 0 if equal,
// +1 if a > b.
//
//	nil < anything else (sorts nulls first, regardless of direction)
//	numeric vs numeric         → numeric compare (incl. string-encoded numbers)
//	bool vs bool               → false < true
//	otherwise                  → string compare on fmt.Sprint
//
// String-encoded numbers ("10" < "9" lexicographically would be the
// surprise) get numeric compare when both sides parse — matters for
// Excel rows where everything arrives as strings.
func compareCells(a, b any) int {
	if a == nil && b == nil {
		return 0
	}
	if a == nil {
		return -1
	}
	if b == nil {
		return 1
	}
	if af, bf, ok := bothNumeric(a, b); ok {
		switch {
		case af < bf:
			return -1
		case af > bf:
			return 1
		default:
			return 0
		}
	}
	if ab, ok := a.(bool); ok {
		if bb, ok2 := b.(bool); ok2 {
			switch {
			case !ab && bb:
				return -1
			case ab && !bb:
				return 1
			default:
				return 0
			}
		}
	}
	as, bs := fmt.Sprint(a), fmt.Sprint(b)
	switch {
	case as < bs:
		return -1
	case as > bs:
		return 1
	default:
		return 0
	}
}

// bothNumeric returns the float64 forms of a and b when both are
// numeric (typed numbers or string-encoded numbers). This is the
// rule that makes Excel-string rows sort sensibly: "10" > "9" instead
// of the lexicographic surprise.
func bothNumeric(a, b any) (float64, float64, bool) {
	af, aok := toFloat(a)
	bf, bok := toFloat(b)
	return af, bf, aok && bok
}

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case int:
		return float64(x), true
	case int8:
		return float64(x), true
	case int16:
		return float64(x), true
	case int32:
		return float64(x), true
	case int64:
		return float64(x), true
	case uint:
		return float64(x), true
	case uint8:
		return float64(x), true
	case uint16:
		return float64(x), true
	case uint32:
		return float64(x), true
	case uint64:
		return float64(x), true
	case float32:
		return float64(x), true
	case float64:
		return x, true
	case string:
		if f, err := strconv.ParseFloat(x, 64); err == nil {
			return f, true
		}
	}
	return 0, false
}
