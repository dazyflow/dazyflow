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
			Description: "Sort rows by one or more columns. The 'by' param is a comma-separated list of column names in priority order — earlier names win, later ones break ties. The Direction param ('sort_dir') sets ascending or descending for the whole sort. To mix directions in a multi-column sort, prefix a name with '-' for descending or '+' for ascending: with Direction ascending, \"revenue,-created_at\" is revenue ascending, then newest first. A prefixed name always keeps its own direction, whatever Direction says. (A legacy array of names / {column,desc:true} objects is still accepted for older flows.)",
			Summary:     "Stably sort rows by one or more columns, ascending or descending, with per-column overrides.",
			Examples: []core.ParamsExample{
				{
					Title:  "Sort by created_at ascending",
					Params: json.RawMessage(`{"by":"created_at"}`),
				},
				{
					Title:  "Newest first",
					Params: json.RawMessage(`{"by":"created_at","sort_dir":"desc"}`),
					Notes:  "Direction applies to every column in 'by'.",
				},
				{
					Title:  "Highest revenue first, then alphabetical name",
					Params: json.RawMessage(`{"by":"-revenue,name"}`),
					Notes:  "Prefix '-' for descending on one column while the rest follow Direction. Multi-key sort is stable: rows with equal revenue keep their name-order tiebreak.",
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
						"description":"Comma-separated column names in priority order; earlier names win ties. E.g. \"revenue,created_at\". To override Direction for one column, prefix it with '-' (descending) or '+' (ascending)."
					},
					"sort_dir":{
						"type":"string",
						"format":"toggle",
						"enum":["asc","desc"],
						"enumNames":["Ascending (A→Z, low→high)","Descending (Z→A, high→low)"],
						"default":"asc",
						"title":"Direction",
						"description":"Which way to sort. Applies to every column in 'Sort by' except ones prefixed with '-' or '+', which keep their own direction."
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
	// explicit records that this key stated its own direction — a '-'/'+'
	// prefix, or a legacy {column,desc} object that carried the key. The
	// Direction param supplies the direction for every key that didn't, so
	// the two settings compose instead of one silently overruling the other.
	explicit bool
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
		return params.Err(job, "bad_param", err.Error()), nil
	}

	descDefault, err := parseSortDir(job.Params)
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	for i := range keys {
		if !keys[i].explicit {
			keys[i].desc = descDefault
		}
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
// comma-separated string ("id,name,-age" → id, name, then age descending); a
// leading '-' or '+' on a name states that column's direction, and anything
// unprefixed takes it from the Direction param. The legacy array form (bare
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
				// A `desc` key that is present — even as false — is a stated
				// direction, so Direction leaves it alone.
				rawDesc, has := it["desc"]
				desc, _ := rawDesc.(bool)
				keys = append(keys, sortKey{column: col, desc: desc, explicit: has})
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

// sortTokenKey turns one token into a key. A leading '-' marks descending
// ("-age") and a leading '+' marks ascending ("+name"); either one is a stated
// direction that the Direction param won't touch. An unprefixed name follows
// Direction. Whitespace is trimmed so "id, -age" splits cleanly. Returns
// ok=false for an empty token. A column literally named with a leading '-' or
// '+' must use the legacy {column,desc} object form.
//
// '+' exists so Direction stays overridable in both directions. Without it,
// "descending, but break ties alphabetically" was unsayable: every unprefixed
// name would follow Direction down, and the only escape hatch ('-') pointed
// the same way.
func sortTokenKey(tok string) (sortKey, bool) {
	tok = strings.TrimSpace(tok)
	desc, explicit := false, false
	switch {
	case strings.HasPrefix(tok, "-"):
		desc, explicit = true, true
		tok = strings.TrimSpace(tok[1:])
	case strings.HasPrefix(tok, "+"):
		desc, explicit = false, true
		tok = strings.TrimSpace(tok[1:])
	}
	if tok == "" {
		return sortKey{}, false
	}
	return sortKey{column: tok, desc: desc, explicit: explicit}, true
}

// parseSortDir reads the Direction param: the direction for every key that
// didn't state one. Unset means ascending, which is what the drop did before
// the param existed.
//
// The long spellings are accepted because they are what a hand-written or
// model-written flow reaches for, and "descending" silently sorting ascending
// is the kind of wrong answer nobody goes looking for. Anything else IS
// reported: a typo'd direction is a flow that quietly returns its rows the
// wrong way round.
func parseSortDir(params map[string]any) (bool, error) {
	raw, ok := params["sort_dir"]
	if !ok || raw == nil {
		return false, nil
	}
	s, ok := raw.(string)
	if !ok {
		return false, fmt.Errorf("sort_dir: expected \"asc\" or \"desc\", got %T", raw)
	}
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "asc", "ascending":
		return false, nil
	case "desc", "descending":
		return true, nil
	default:
		return false, fmt.Errorf("sort_dir: expected \"asc\" or \"desc\", got %q", s)
	}
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
