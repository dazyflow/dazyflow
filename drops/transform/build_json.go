// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package transform

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "build_json",
			Version:     "1.0",
			Label:       "Write JSON",
			Subtitle:    "Rows into JSON text",
			Icon:        "braces",
			Category:    "transformation",
			Provider:    "internal",
			Tags:        []string{"transform", "json", "export", "rows", "etl", "serialize", "api"},
			Description: "Turn rows into JSON text — the inverse of Read JSON. Connect the rows from a DB query, a Sheets read or any transform and get a JSON string to POST to an API, write to a file, or attach to an email. Rows become an array of objects; set 'single object' when there's exactly one row and the API wants the object itself rather than a list of one. 'columns' picks or reorders the fields. Use this rather than building JSON in a template: an apostrophe or a quote in a customer's name breaks hand-written JSON and is escaped correctly here.",
			Summary:     "Serialize rows into a JSON string for APIs, files, or email attachments.",
			Examples: []core.ParamsExample{
				{
					Title:  "Rows to a JSON array",
					Params: json.RawMessage(`{}`),
					Notes:  "Connect the result into an HTTP request's body.",
				},
				{
					Title:  "One row as a bare object, pretty-printed",
					Params: json.RawMessage(`{"single":true,"indent":true}`),
					Notes:  "For an API that wants {\"name\":…} rather than [{\"name\":…}].",
				},
				{
					Title:  "Pick and order the fields",
					Params: json.RawMessage(`{"columns":["id","name","email"]}`),
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "rows", Label: "Rows", Required: true, MIME: []string{"application/json"}},
			},
			Outputs: []core.Port{
				// Text, not application/json: the point of this step is a
				// STRING to put in a body or a file. A port that already
				// carries structured JSON needs no serialising.
				{Port: "out", Label: "JSON", MIME: []string{"text/plain", "application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"single":{"type":"boolean","default":false,"title":"Single object","description":"When on and there is exactly one row, emit that object on its own instead of an array holding it. What most APIs that create one thing expect."},
					"indent":{"type":"boolean","default":false,"title":"Pretty-print","description":"Lay the JSON out over several lines. Easier to read in a file or a log; leave off for an API body."},
					"columns":{"type":"array","items":{"type":"string"},"title":"Columns","description":"Optional explicit field order/subset. When empty, the rows' own column order is used."}
				}
			}`),
			Idempotent: true,
		},
		Execute: executeBuildJSON,
	})
}

func executeBuildJSON(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	rows, headers, errRes, ok := loadRowsAndHeaders(job)
	if !ok {
		return errRes, nil
	}
	cols, errRes, ok := chosenColumns(job, headers)
	if !ok {
		return errRes, nil
	}

	projected := projectRows(rows, cols)

	var payload any = projected
	if params.BoolDefault(job.Params, "single", false) && len(projected) == 1 {
		payload = projected[0]
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	// HTML escaping off: Go's encoder turns < > & into < and friends by
	// default, which is right for embedding in a page and wrong for an API
	// body — a URL or a "Smith & Sons" arrives mangled to anything reading
	// the raw text.
	enc.SetEscapeHTML(false)
	if params.BoolDefault(job.Params, "indent", false) {
		enc.SetIndent("", "  ")
	}
	if err := enc.Encode(payload); err != nil {
		return params.Err(job, "bad_input", "those rows can't be written as JSON: "+err.Error()), nil
	}
	// Encode appends a newline; a body is cleaner without it.
	out := bytes.TrimRight(buf.Bytes(), "\n")

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"out": {MIME: "application/json", Inline: string(out)},
		},
	}, nil
}

// chosenColumns resolves the column order: the 'columns' param when set,
// otherwise the rows' own headers. Shared by the three writers so they can't
// drift on what "columns" means.
func chosenColumns(job core.Job, headers []string) ([]string, core.Result, bool) {
	raw, present := job.Params["columns"]
	if !present {
		return headers, core.Result{}, true
	}
	cols, err := normalizeStringSlice(raw, "columns")
	if err != nil {
		return nil, params.Err(job, "bad_param", err.Error()), false
	}
	if len(cols) == 0 {
		return headers, core.Result{}, true
	}
	return cols, core.Result{}, true
}

// projectRows narrows and orders each row to cols. An empty cols means "every
// field, as it came" — the rows pass through untouched, which keeps a nested
// value nested rather than flattening it.
func projectRows(rows []map[string]any, cols []string) []map[string]any {
	if len(cols) == 0 {
		if rows == nil {
			return []map[string]any{}
		}
		return rows
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		next := make(map[string]any, len(cols))
		for _, c := range cols {
			// A column the rows don't have becomes null rather than being
			// omitted: an API validating a schema wants the key present, and
			// a silently missing field is harder to notice than a null.
			next[c] = row[c]
		}
		out = append(out, next)
	}
	return out
}
