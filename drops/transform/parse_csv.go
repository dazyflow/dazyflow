// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package transform

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "parse_csv",
			Version:     "1.0",
			Label:       "Read CSV",
			Subtitle:    "CSV text into rows",
			Icon:        "table",
			Category:    "transformation",
			Provider:    "internal",
			Tags:        []string{"transform", "csv", "parse", "rows", "etl", "import"},
			Description: "Turn CSV text into rows. Feed it an HTTP response, a downloaded file's contents, or any comma-separated text and it parses into the standard rows + headers shape that Sheets, Excel, Postgres, and the transform family consume. By default the first line is the header row and names the columns; set 'header' false for headerless data (columns become col1, col2, …). 'delimiter' switches the separator — use \"\\t\" or \"tab\" for tab-separated values, \";\" for European CSVs. Rows shorter than the header are padded with empty strings; longer rows keep their extra cells under the padded names.",
			Summary:     "Parse CSV/TSV text into rows + headers for the table steps that follow.",
			Examples: []core.ParamsExample{
				{
					Title:  "Parse a CSV download straight into rows",
					Params: json.RawMessage(`{}`),
					Notes:  "Connect the file/HTTP text into 'in'. The first line is treated as the header.",
				},
				{
					Title:  "Tab-separated, no header row",
					Params: json.RawMessage(`{"delimiter":"tab","header":false}`),
					Notes:  "Columns are named col1, col2, … when there's no header line.",
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "in", Label: "CSV", Required: true, MIME: []string{"text/csv", "text/plain"}},
			},
			Outputs: []core.Port{
				{Port: "rows", Label: "Rows", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"delimiter":{"type":"string","default":",","title":"Delimiter","description":"Field separator. Use \"\\t\" or \"tab\" for TSV, \";\" for European CSVs. A single character."},
					"header":{"type":"boolean","default":true,"title":"First row is header","description":"When true the first line names the columns; when false columns are named col1, col2, …"}
				}
			}`),
			Idempotent: true,
		},
		Execute: executeParseCSV,
	})
}

// executeParseCSV reads the 'in' text as CSV and emits rows. The input is
// the raw string an HTTP/file drop produced; a non-string inline value is
// rejected (use parse_json for already-structured data).
func executeParseCSV(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	ref, ok := job.Input["in"]
	if !ok {
		return params.Err(job, "missing_input", "input port 'in' is required"), nil
	}
	text, ok := ref.Inline.(string)
	if !ok {
		return params.Err(job, "bad_input", fmt.Sprintf("expected CSV text, got %T — use parse_json for structured data", ref.Inline)), nil
	}
	if strings.TrimSpace(text) == "" {
		return params.Err(job, "bad_input", "input 'in' is empty"), nil
	}

	comma, err := csvDelimiter(job.Params)
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	hasHeader := paramBoolDefault(job.Params, "header", true)

	r := csv.NewReader(strings.NewReader(text))
	r.Comma = comma
	r.FieldsPerRecord = -1 // tolerate ragged rows; we pad/keep below
	r.TrimLeadingSpace = true

	records, err := r.ReadAll()
	if err != nil {
		return params.Err(job, "bad_input", "input is not valid CSV: "+err.Error()), nil
	}
	if len(records) == 0 {
		return resultRows(job, []map[string]any{}, nil), nil
	}

	var headers []string
	var dataRows [][]string
	if hasHeader {
		headers = records[0]
		dataRows = records[1:]
	} else {
		width := 0
		for _, rec := range records {
			if len(rec) > width {
				width = len(rec)
			}
		}
		headers = make([]string, width)
		for i := range headers {
			headers[i] = fmt.Sprintf("col%d", i+1)
		}
		dataRows = records
	}

	if err := capRows(len(dataRows)); err != nil {
		return params.Err(job, "too_large", err.Error()), nil
	}

	out := make([]map[string]any, 0, len(dataRows))
	for _, rec := range dataRows {
		row := make(map[string]any, len(headers))
		for i, h := range headers {
			if i < len(rec) {
				row[h] = rec[i]
			} else {
				row[h] = "" // pad short rows so every row has every column
			}
		}
		// Cells beyond the header width (ragged long rows) keep a synthetic name.
		for i := len(headers); i < len(rec); i++ {
			row[fmt.Sprintf("col%d", i+1)] = rec[i]
		}
		out = append(out, row)
	}

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"rows": {MIME: "application/json", Inline: out, Headers: headers},
		},
	}, nil
}

// csvDelimiter reads the 'delimiter' param into a single rune, accepting the
// friendly aliases "tab"/"\t" for a tab. Defaults to comma.
func csvDelimiter(p map[string]any) (rune, error) {
	d, _ := p["delimiter"].(string)
	switch d {
	case "", ",":
		return ',', nil
	case "\\t", "tab", "\t":
		return '\t', nil
	}
	rs := []rune(d)
	if len(rs) != 1 {
		return 0, fmt.Errorf("delimiter must be a single character (or \"tab\"), got %q", d)
	}
	return rs[0], nil
}

// paramBoolDefault reads a bool param, treating a missing/non-bool value as
// def. Kept local so the CSV drops don't reach across into the params pkg for
// a one-liner (transform already avoids that dependency).
func paramBoolDefault(p map[string]any, key string, def bool) bool {
	if v, ok := p[key].(bool); ok {
		return v
	}
	return def
}
