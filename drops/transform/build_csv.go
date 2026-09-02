// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package transform

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"strings"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "build_csv",
			Version:     "1.0",
			Label:       "Write CSV",
			Subtitle:    "Rows into CSV text",
			Icon:        "table-2",
			Category:    "transformation",
			Provider:    "internal",
			Tags:        []string{"transform", "csv", "export", "rows", "etl", "serialize"},
			Description: "Turn rows into CSV text — the inverse of Read CSV. Connect in the rows from a DB query, a Sheets/Excel read, or any transform, and get a single CSV string you can attach to an email, write to a file, or POST to an API. Columns follow the rows' own column order; set 'columns' to pick or reorder a subset. 'delimiter' switches the separator (\"\\t\"/\"tab\" for TSV, \";\" for European CSVs), and 'header' toggles the header line.",
			Summary:     "Serialize rows into a CSV/TSV string for files, email attachments, or APIs.",
			Examples: []core.ParamsExample{
				{
					Title:  "Rows to a CSV string",
					Params: json.RawMessage(`{}`),
					Notes:  "Column order comes from the incoming rows; header row included by default.",
				},
				{
					Title:  "Pick and order specific columns as TSV",
					Params: json.RawMessage(`{"columns":["name","email","signed_up"],"delimiter":"tab"}`),
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "rows", Label: "Rows", Required: true, MIME: []string{"application/json"}},
			},
			Outputs: []core.Port{
				{Port: "out", Label: "CSV", MIME: []string{"text/csv"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"delimiter":{"type":"string","default":",","title":"Delimiter","description":"Field separator. Use \"\\t\" or \"tab\" for TSV, \";\" for European CSVs. A single character."},
					"header":{"type":"boolean","default":true,"title":"Include header row","description":"When true the first line lists the column names."},
					"columns":{"type":"array","items":{"type":"string"},"title":"Columns","description":"Optional explicit column order/subset. When empty, the rows' own column order is used."}
				}
			}`),
			Idempotent: true,
		},
		Execute: executeBuildCSV,
	})
}

// executeBuildCSV serializes the 'rows' input to a CSV string. Column order
// comes from the folded headers on the rows value (or is derived), unless the
// 'columns' param overrides it.
func executeBuildCSV(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	rowsOut, headers, errRes, ok := loadRowsAndHeaders(job)
	if !ok {
		return errRes, nil
	}

	if raw, present := job.Params["columns"]; present {
		cols, err := normalizeStringSlice(raw, "columns")
		if err != nil {
			return params.Err(job, "bad_param", err.Error()), nil
		}
		if len(cols) > 0 {
			headers = cols
		}
	}

	comma, err := csvDelimiter(job.Params)
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	includeHeader := paramBoolDefault(job.Params, "header", true)

	var buf strings.Builder
	w := csv.NewWriter(&buf)
	w.Comma = comma

	if includeHeader {
		if err := w.Write(headers); err != nil {
			return params.Err(job, "encode_failed", err.Error()), nil
		}
	}
	rec := make([]string, len(headers))
	for _, row := range rowsOut {
		for i, h := range headers {
			rec[i] = cellString(row[h])
		}
		if err := w.Write(rec); err != nil {
			return params.Err(job, "encode_failed", err.Error()), nil
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return params.Err(job, "encode_failed", err.Error()), nil
	}

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"out": {MIME: "text/csv", Inline: buf.String()},
		},
	}, nil
}
