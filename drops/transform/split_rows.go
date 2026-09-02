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
			ID:          "split_rows",
			Version:     "1.0",
			Label:       "Split rows",
			Icon:        "git-branch",
			Category:    "transformation",
			Provider:    "internal",
			Tags:        []string{"transform", "split", "fork", "branch", "filter", "etl"},
			Description: "Fork a row stream into two by a CEL predicate. Rows where the filter evaluates to true go out 'matched'; the rest go out 'unmatched'. Same formula as the Add a calculated column step's filter — `row.active && row.score >= 50` and similar. Use when you'd otherwise need Choose & rename columns twice (once with the filter, once with its negation): this step walks the input once and gives you both halves at the cost of nothing extra.",
			Summary:     "Fork a row stream into matched/unmatched halves using a single CEL boolean expression.",
			Examples: []core.ParamsExample{
				{
					Title:  "Split valid vs invalid records",
					Params: json.RawMessage(`{"filter":"row.email != '' && row.age >= 18"}`),
					Notes:  "Use the unmatched port to connect rows into a dead-letter table or review queue.",
				},
				{
					Title:  "Active premium customers vs everyone else",
					Params: json.RawMessage(`{"filter":"row.status == 'active' && row.plan == 'premium'"}`),
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "rows", Label: "Rows", Required: true, MIME: []string{"application/json"}},
			},
			Outputs: []core.Port{
				{Port: "matched", Label: "Matched", MIME: []string{"application/json"}, List: true},
				{Port: "unmatched", Label: "Unmatched", MIME: []string{"application/json"}, List: true},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"filter":{"type":"string","format":"row-condition","description":"CEL expression that must return a bool. Rows where it's true land on 'matched', false on 'unmatched'."}
				},
				"required":["filter"]
			}`),
			Idempotent: true,
		},
		Execute: executeSplitRows,
	})
}

// executeSplitRows is the "filter that doesn't drop the false branch"
// drop. Real ETL pipelines want to route invalid records somewhere
// (review queue, dead-letter table, log file) rather than dropping
// them — split_rows makes that a one-node pattern instead of the
// two-map_rows-with-opposite-filters workaround that walks the
// input twice.
//
// Filter semantics match compute_rows.filter exactly (same CEL env,
// same evalFilter helper, same "expression must return bool" rule).
// Runtime errors fail the whole batch, consistent with compute_rows
// and the SQL drops — partial routing is worse than no routing for
// downstream consumers expecting deterministic split sizes.
func executeSplitRows(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	rows, headers, errRes, ok := loadRowsAndHeaders(job)
	if !ok {
		return errRes, nil
	}

	env, err := newRowCELEnv()
	if err != nil {
		return params.Err(job, "internal", fmt.Sprintf("cel env: %v", err)), nil
	}
	prog, err := compileOptionalFilter(env, job.Params)
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	if prog == nil {
		return params.Err(job, "bad_param", "filter: required"), nil
	}

	// Pre-size each side optimistically; if the split is skewed,
	// one slice grows and the other stays small — slightly wasteful
	// in the worst case but avoids repeated append-grows on the
	// common balanced case.
	matched := make([]map[string]any, 0, len(rows)/2+1)
	unmatched := make([]map[string]any, 0, len(rows)/2+1)
	for i, row := range rows {
		pass, err := evalFilter(ctx, prog, row)
		if err != nil {
			return params.Err(job, "eval", fmt.Sprintf("filter row %d: %v", i, err)), nil
		}
		if pass {
			matched = append(matched, row)
		} else {
			unmatched = append(unmatched, row)
		}
	}

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"matched":   {MIME: "application/json", Inline: matched, Headers: headers},
			"unmatched": {MIME: "application/json", Inline: unmatched, Headers: headers},
		},
	}, nil
}
