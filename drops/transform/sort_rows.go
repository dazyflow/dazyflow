// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package transform

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "sort_rows",
			Version:     "1.0",
			Label:       "Sort rows",
			Icon:        "cpu",
			Category:    "transformation",
			Provider:    "internal",
			Tags:        []string{"transform", "sort", "order", "etl"},
			Description: "Sort rows by one or more columns. The 'by' param is a comma-separated list of column names in priority order — earlier names win, later ones break ties. Prefix a name with '-' for descending: \"revenue,-created_at\" is revenue ascending, then newest first. (A legacy array of names / {column,desc:true} objects is still accepted for older flows.)",
			Summary:     "Stably sort rows by one or more columns, ascending or '-descending' per key.",
			Examples: []core.ParamsExample{
				{
					Title:  "Sort by created_at ascending",
					Params: json.RawMessage(`{"by":"created_at"}`),
				},
				{
					Title:  "Highest revenue first, then alphabetical name",
					Params: json.RawMessage(`{"by":"-revenue,name"}`),
					Notes:  "Prefix '-' for descending. Multi-key sort is stable: rows with equal revenue keep their name-order tiebreak.",
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
					"by":{
						"type":"string",
						"title":"Sort by",
						"description":"Comma-separated column names in priority order; earlier names win ties. Prefix a name with '-' for descending. E.g. \"revenue,-created_at\"."
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
	rows, headers, errRes, ok := loadRowsAndHeaders(job)
	if !ok {
		return errRes, nil
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

	return resultRows(job, out, headers), nil
}

// parseSortKeys reads the `by` param. The documented shape is a single
// comma-separated string ("id,name,-age" → id asc, name asc, age desc); a
// leading '-' on a name flips it to descending. The legacy array form (bare
// strings and {column,desc} objects) is still accepted so older flows keep
// working. No column-type auto-detect here — that happens lazily in the
// comparator.
func parseSortKeys(params map[string]any) ([]sortKey, error) {
	raw, ok := params["by"]
	if !ok || raw == nil {
		return nil, fmt.Errorf("by: required")
	}
	switch v := raw.(type) {
	case string:
		return parseSortString(v)
	case []string:
		// Native callers (tests) may pass []string directly.
		keys := make([]sortKey, 0, len(v))
		for _, s := range v {
			if k, ok := sortTokenKey(s); ok {
				keys = append(keys, k)
			}
		}
		if len(keys) == 0 {
			return nil, fmt.Errorf("by: must list at least one column")
		}
		return keys, nil
	case []any:
		if len(v) == 0 {
			return nil, fmt.Errorf("by: must list at least one key")
		}
		keys := make([]sortKey, 0, len(v))
		for i, item := range v {
			switch it := item.(type) {
			case string:
				if k, ok := sortTokenKey(it); ok {
					keys = append(keys, k)
				}
			case map[string]any:
				col, _ := it["column"].(string)
				if col == "" {
					return nil, fmt.Errorf("by[%d]: 'column' missing", i)
				}
				desc, _ := it["desc"].(bool)
				keys = append(keys, sortKey{column: col, desc: desc})
			default:
				return nil, fmt.Errorf("by[%d]: expected string or {column,desc}, got %T", i, item)
			}
		}
		if len(keys) == 0 {
			return nil, fmt.Errorf("by: must list at least one column")
		}
		return keys, nil
	default:
		return nil, fmt.Errorf("by: expected a comma-separated string or array, got %T", raw)
	}
}

// parseSortString splits "id,name,-age" into ordered keys. Empty tokens (a
// stray comma) are skipped.
func parseSortString(s string) ([]sortKey, error) {
	keys := make([]sortKey, 0)
	for _, part := range strings.Split(s, ",") {
		if k, ok := sortTokenKey(part); ok {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("by: must list at least one column")
	}
	return keys, nil
}

// sortTokenKey turns one token into a key: a leading '-' marks descending
// ("-age"), otherwise ascending. Whitespace is trimmed so "id, -age" splits
// cleanly. Returns ok=false for an empty token. A column literally named with
// a leading '-' must use the legacy {column,desc} object form.
func sortTokenKey(tok string) (sortKey, bool) {
	tok = strings.TrimSpace(tok)
	desc := false
	if strings.HasPrefix(tok, "-") {
		desc = true
		tok = strings.TrimSpace(tok[1:])
	}
	if tok == "" {
		return sortKey{}, false
	}
	return sortKey{column: tok, desc: desc}, true
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
