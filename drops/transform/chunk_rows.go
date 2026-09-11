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

const (
	defaultChunkSize = 100
	maxChunkSize     = 10_000
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:       "chunk_rows",
			Version:  "1.0",
			Label:    "Batch rows",
			Subtitle: "Into groups of N",
			Icon:     "layers",
			Category: "transformation",
			Provider: "internal",
			Tags: []string{"batch", "chunk", "group", "split", "bulk", "pages",
				"n at a time", "transform", "etl"},
			Description: "Cuts a long list into batches of a fixed size, so the steps after it work on groups rather than on one row at a time.\n\nThis is what an API with a bulk endpoint wants: a thousand rows as ten calls of a hundred, not a thousand calls. Pair it with For each — one iteration per batch — and the batch's rows are on `rows` inside it. It is also how a long list fits inside a model's context: a hundred rows a time through an AI step, rather than one prompt nothing can read.\n\nEach batch carries its position and its size alongside its rows, so a subject line or a log entry can say \"part 3 of 10\" without counting anything itself. The last batch is whatever is left over — batches are equal except that one.",
			Summary:     "Cut a list into batches of N rows for bulk APIs, chunked AI prompts and paged sends.",
			Examples: []core.ParamsExample{
				{
					Title:  "A hundred rows per API call",
					Params: json.RawMessage(`{"size":100}`),
					Notes:  "Feed the batches into For each; inside the loop, the batch's rows are on 'rows'.",
				},
				{
					Title:  "Twenty at a time into an AI step",
					Params: json.RawMessage(`{"size":20}`),
					Notes:  "Keeps each prompt inside the model's context, and each answer about a readable number of rows.",
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "rows", Label: "Rows", Required: true, MIME: []string{"application/json"}},
			},
			Outputs: []core.Port{
				{Port: "batches", Label: "Batches", MIME: []string{"application/json"}, List: true,
					Example: json.RawMessage(`[
						{"index":0,"number":1,"of":2,"size":2,"rows":[{"id":"a"},{"id":"b"}]},
						{"index":1,"number":2,"of":2,"size":1,"rows":[{"id":"c"}]}
					]`)},
				{Port: "count", Label: "Batch count", MIME: []string{"text/plain"}, Example: json.RawMessage(`"2"`)},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"size":{
						"type":"integer",
						"title":"Rows per batch",
						"default":100,
						"minimum":1,
						"maximum":10000,
						"description":"How many rows go in each batch. The last one takes whatever is left over."
					}
				}
			}`),
			Idempotent: true,
		},
		Execute: executeChunkRows,
	})
}

func executeChunkRows(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	rows, headers, errRes, ok := loadRowsAndHeaders(job)
	if !ok {
		return errRes, nil
	}
	size := params.ClampInt(params.IntDefault(job.Params, "size", defaultChunkSize), 1, maxChunkSize)

	total := (len(rows) + size - 1) / size
	batches := make([]any, 0, total)
	for start := 0; start < len(rows); start += size {
		end := start + size
		if end > len(rows) {
			end = len(rows)
		}
		// Each batch is an object rather than a bare list: a port carrying
		// lists-of-lists has no field names for a reference to reach into, and
		// "part 3 of 10" is the first thing a batched send wants to say.
		batches = append(batches, map[string]any{
			"index":  len(batches),
			"number": len(batches) + 1,
			"of":     total,
			"size":   end - start,
			"rows":   rows[start:end],
		})
	}

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			// Headers travel on the batches so the columns survive the cut —
			// the steps inside the loop see the same order the source had.
			"batches": {MIME: "application/json", Inline: batches, Headers: headers},
			"count":   {MIME: "text/plain", Inline: fmt.Sprint(len(batches))},
		},
	}, nil
}
