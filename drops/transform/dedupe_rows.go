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
			ID:          "dedupe_rows",
			Version:     "1.0",
			Label:       "Remove duplicates",
			Icon:        "cpu",
			Category:    "transformation",
			Provider:    "internal",
			Tags:        []string{"transform", "dedupe", "unique", "etl"},
			Description: "Drop duplicate rows. By default, two rows are duplicates when every cell matches; with 'by' set, duplicates share only the listed columns. 'keep' picks which copy of a duplicate group survives: \"first\" (default) or \"last\". Preserves input order for the surviving rows.",
			Summary:     "Drop duplicate rows, optionally keyed on a subset of columns, keeping the first or last copy.",
			Examples: []core.ParamsExample{
				{
					Title:  "Dedupe on full row equality",
					Params: json.RawMessage(`{}`),
					Notes:  "With no 'by', two rows are duplicates only when every cell matches.",
				},
				{
					Title:  "Dedupe by email, keep the most recent",
					Params: json.RawMessage(`{"by":["email"],"keep":"last"}`),
				},
				{
					Title:  "Composite key dedupe",
					Params: json.RawMessage(`{"by":["tenant_id","sku"],"keep":"first"}`),
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "rows", Label: "Rows", Required: true, MIME: []string{"application/json"}},
			},
			Outputs: []core.Port{
				{Port: "rows", Label: "Rows", MIME: []string{"application/json"}},
				{Port: "dropped", Label: "Duplicate count", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"by":{
						"type":"array",
						"items":{"type":"string"},
						"description":"Columns that define duplicate identity. Absent = use every column in the headers (or, if no headers, every key of the row)."
					},
					"keep":{
						"type":"string",
						"title":"Keep",
						"enum":["first","last"],
						"enumNames":["The first one","The last one"],
						"default":"first",
						"description":"Which member of a duplicate group survives."
					}
				}
			}`),
			Idempotent: true,
		},
		Execute: executeDedupeRows,
	})
}

// executeDedupeRows preserves the input order of the surviving rows.
// "first" walks forward and emits on first sight; "last" walks
// backward, emits on first sight, then reverses. Both modes use a
// single-pass seen map keyed by a delimited join of the cell values
// (fmt.Sprint per cell) — cheap, correct enough for ETL row
// identities, and matches the lenient equality used elsewhere in
// the package.
func executeDedupeRows(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	rows, headers, errRes, ok := loadRowsAndHeaders(job)
	if !ok {
		return errRes, nil
	}

	by, err := parseDedupeBy(job.Params, headers, rows)
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	keep, err := parseKeep(job.Params)
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}

	out := make([]map[string]any, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))

	switch keep {
	case "first":
		for _, row := range rows {
			k := keyString(row, by)
			if _, ok := seen[k]; ok {
				continue
			}
			seen[k] = struct{}{}
			out = append(out, row)
		}
	case "last":
		// Walk backward marking seen; then walk forward emitting only
		// rows whose key wasn't passed-over earlier. The two passes
		// keep the survivors in original order — "keep the last copy
		// of each group, but don't reorder the result."
		toEmit := make(map[int]struct{}, len(rows))
		for i := len(rows) - 1; i >= 0; i-- {
			k := keyString(rows[i], by)
			if _, ok := seen[k]; ok {
				continue
			}
			seen[k] = struct{}{}
			toEmit[i] = struct{}{}
		}
		for i, row := range rows {
			if _, ok := toEmit[i]; ok {
				out = append(out, row)
			}
		}
	}

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"rows":    {MIME: "application/json", Inline: out, Headers: headers},
			"dropped": {MIME: "application/json", Inline: len(rows) - len(out)},
		},
	}, nil
}

// parseDedupeBy resolves which columns form the dedupe identity.
// Empty/absent param falls back to all headers; if headers are empty
// too (no schema known yet), the row's own keys define identity per
// row — sorted so a row with the same content always produces the
// same key regardless of map iteration order.
func parseDedupeBy(params map[string]any, headers []string, rows []map[string]any) ([]string, error) {
	raw, ok := params["by"]
	if !ok || raw == nil {
		if len(headers) > 0 {
			return headers, nil
		}
		// No explicit dedupe keys AND no headers: union of keys
		// across all rows, sorted (same logic as deriveHeaders).
		return deriveHeaders(rows), nil
	}
	switch v := raw.(type) {
	case []string:
		return v, nil
	case []any:
		out := make([]string, 0, len(v))
		for i, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("by[%d]: expected string, got %T", i, item)
			}
			out = append(out, s)
		}
		return out, nil
	}
	return nil, fmt.Errorf("by: expected array of strings, got %T", raw)
}

func parseKeep(params map[string]any) (string, error) {
	raw, ok := params["keep"]
	if !ok || raw == nil {
		return "first", nil
	}
	s, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("keep: expected string, got %T", raw)
	}
	switch s {
	case "", "first":
		return "first", nil
	case "last":
		return "last", nil
	}
	return "", fmt.Errorf("keep: expected \"first\" or \"last\", got %q", s)
}
