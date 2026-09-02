// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package transform

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "map_rows",
			Version:     "1.0",
			Label:       "Choose & rename columns",
			Icon:        "cpu",
			Category:    "transformation",
			Provider:    "internal",
			Tags:        []string{"transform", "map", "rename", "filter", "select", "etl"},
			Description: "Reshape a row stream between two steps: select/drop columns, rename, fill missing values, filter rows on equality / inequality / membership. All operations refer to INPUT column names; renames apply last so the output uses the renamed names. Pure config, no expression language — covers the bulk of 'my Excel columns don't match my DB schema' cases.",
			Summary:     "Reshape rows: select or drop columns, rename, default missing cells, and filter by equality or membership.",
			Examples: []core.ParamsExample{
				{
					Title:  "Select and rename columns",
					Params: json.RawMessage(`{"select":["id","first_name","last_name","email"],"rename":{"first_name":"given_name","last_name":"family_name"}}`),
				},
				{
					Title:  "Keep only active SE users, default missing country",
					Params: json.RawMessage(`{"filter_eq":{"status":"active"},"filter_in":{"country":["SE","NO","DK"]},"default":{"country":"SE"}}`),
					Notes:  "Comparisons are string-based, so an int 30 and the string \"30\" match.",
				},
				{
					Title:  "Drop sensitive columns before fanout",
					Params: json.RawMessage(`{"drop":["ssn","internal_notes"]}`),
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
					"select":     {"type":"array","items":{"type":"string"},"description":"Columns to keep (mutually exclusive with drop). If absent and drop is absent, all input columns are kept."},
					"drop":       {"type":"array","items":{"type":"string"},"description":"Columns to remove (mutually exclusive with select)."},
					"rename":     {"type":"object","additionalProperties":{"type":"string"},"description":"Column rename map {old_name: new_name}. Applied AFTER select/drop/default so other ops can still refer to original names."},
					"default":    {"type":"object","additionalProperties":true,"description":"Default values for missing or null cells, keyed by INPUT column name."},
					"filter_eq":  {"type":"object","additionalProperties":true,"description":"Keep only rows where every listed (column == value). String-compared, so 30 matches \"30\"."},
					"filter_neq": {"type":"object","additionalProperties":true,"description":"Drop rows where any listed (column == value). String-compared."},
					"filter_in":  {"type":"object","additionalProperties":{"type":"array"},"description":"Keep only rows where every listed column's value appears in the given list. String-compared."}
				}
			}`),
			Idempotent: true,
		},
		Execute: executeMapRows,
	})
}

// mapSpec is the parsed, validated form of the drop's params. We
// parse once up front so the per-row hot loop doesn't re-validate
// shapes on every iteration.
type mapSpec struct {
	hasSelect bool
	hasDrop   bool
	selectCol []string
	dropCol   []string
	rename    map[string]string
	defaults  map[string]any
	filterEq  map[string]any
	filterNeq map[string]any
	filterIn  map[string][]any
}

// executeMapRows applies a static row-transformation spec. The order
// of operations is fixed (filter → select/drop → default → rename)
// and ALL operation keys refer to INPUT column names — rename
// happens last and only affects the output, so an entry like
// `filter_eq: {status: "active"}` always means "the input column
// named status", not whatever you renamed it to.
//
// This is the explicit no-expression-language design: less power
// than a full eval but no sandboxing concern, no surprises around
// scope, and the params schema is itself a contract. When the
// expressive ceiling becomes the bottleneck (string concat,
// arithmetic, conditional defaults), the right answer is a sibling
// `compute_rows` drop backed by CEL or similar — not turning this
// one into a partial expression evaluator.
func executeMapRows(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	rows, inputHeaders, errRes, ok := loadRowsAndHeaders(job)
	if !ok {
		return errRes, nil
	}

	spec, err := parseMapSpec(job.Params)
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}

	// 1. Filter rows. Reduces the working set before we do per-cell
	// work in the projection step below.
	filtered := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		if rowPassesFilters(row, spec) {
			filtered = append(filtered, row)
		}
	}

	// 2. Decide the kept column list. select wins if present;
	// otherwise drop subtracts from input headers; otherwise all.
	var keptCols []string
	switch {
	case spec.hasSelect:
		keptCols = spec.selectCol
	case spec.hasDrop:
		skip := make(map[string]struct{}, len(spec.dropCol))
		for _, c := range spec.dropCol {
			skip[c] = struct{}{}
		}
		keptCols = make([]string, 0, len(inputHeaders))
		for _, h := range inputHeaders {
			if _, ok := skip[h]; !ok {
				keptCols = append(keptCols, h)
			}
		}
	default:
		keptCols = inputHeaders
	}

	// 3. Resolve output headers (kept cols with renames applied).
	outputHeaders := make([]string, len(keptCols))
	for i, c := range keptCols {
		if newName, ok := spec.rename[c]; ok {
			outputHeaders[i] = newName
		} else {
			outputHeaders[i] = c
		}
	}

	// 4. Project each filtered row into the kept columns, applying
	// defaults for missing/null values, then write under the output
	// (renamed) name.
	outRows := make([]map[string]any, 0, len(filtered))
	for _, row := range filtered {
		outRow := make(map[string]any, len(keptCols))
		for i, c := range keptCols {
			val, present := row[c]
			if !present || val == nil {
				if def, ok := spec.defaults[c]; ok {
					val = def
				}
			}
			outRow[outputHeaders[i]] = val
		}
		outRows = append(outRows, outRow)
	}

	return resultRows(job, outRows, outputHeaders), nil
}

func parseMapSpec(params map[string]any) (mapSpec, error) {
	var spec mapSpec
	if v, ok := params["select"]; ok {
		s, err := normalizeStringSlice(v, "select")
		if err != nil {
			return spec, err
		}
		spec.selectCol = s
		spec.hasSelect = true
	}
	if v, ok := params["drop"]; ok {
		s, err := normalizeStringSlice(v, "drop")
		if err != nil {
			return spec, err
		}
		spec.dropCol = s
		spec.hasDrop = true
	}
	if spec.hasSelect && spec.hasDrop {
		return spec, fmt.Errorf("select and drop are mutually exclusive")
	}
	if v, ok := params["rename"]; ok {
		m, err := normalizeStringMap(v, "rename")
		if err != nil {
			return spec, err
		}
		spec.rename = m
	}
	if v, ok := params["default"]; ok {
		m, err := normalizeAnyMap(v, "default")
		if err != nil {
			return spec, err
		}
		spec.defaults = m
	}
	if v, ok := params["filter_eq"]; ok {
		m, err := normalizeAnyMap(v, "filter_eq")
		if err != nil {
			return spec, err
		}
		spec.filterEq = m
	}
	if v, ok := params["filter_neq"]; ok {
		m, err := normalizeAnyMap(v, "filter_neq")
		if err != nil {
			return spec, err
		}
		spec.filterNeq = m
	}
	if v, ok := params["filter_in"]; ok {
		m, err := normalizeAnyArrayMap(v, "filter_in")
		if err != nil {
			return spec, err
		}
		spec.filterIn = m
	}
	return spec, nil
}

// rowPassesFilters returns true if the row survives every active
// filter. Comparison is string-based via fmt.Sprint so int 30 and
// the string "30" compare equal — matches the reality that Excel
// rows arrive as strings and typed DB rows as native types, and
// users almost never want the strict-equality footgun.
func rowPassesFilters(row map[string]any, spec mapSpec) bool {
	for col, want := range spec.filterEq {
		if !equalAsString(row[col], want) {
			return false
		}
	}
	for col, drop := range spec.filterNeq {
		if equalAsString(row[col], drop) {
			return false
		}
	}
	for col, allowed := range spec.filterIn {
		got := row[col]
		match := false
		for _, a := range allowed {
			if equalAsString(got, a) {
				match = true
				break
			}
		}
		if !match {
			return false
		}
	}
	return true
}

func equalAsString(a, b any) bool {
	return fmt.Sprint(a) == fmt.Sprint(b)
}
