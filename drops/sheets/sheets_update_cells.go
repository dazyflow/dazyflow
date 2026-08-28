// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package sheets

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
	"git.sr.ht/~klahr/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "sheets_update_cells",
			Version:     "1.0",
			Label:       "Google Sheets",
			Subtitle:    "Update cells",
			Summary:     "Write values back into rows a Read step returned — marking each one done.",
			Description: "Change cells in rows that are already in the sheet, instead of adding new ones. Turn on 'Include row numbers' in the Read range step, keep the rows you acted on, and send them here with the columns you want changed — each row is written back to the row it came from. This is how a flow marks work as done (Status = Invoiced, Reminded on = today) so the next run skips it. Columns not listed are left alone, and a column the sheet doesn't have yet is added at the end.",
			Integration: "Google Sheets",
			Category:    "network",
			Icon:        "sheets",
			BrandLogo:   "/brands/google-sheets.svg",
			Color:       "#0F9D58",
			Provider:    "internal",
			Tags:        []string{"sheets", "google", "update", "write", "mark", "status"},
			Examples: []core.ParamsExample{
				{
					Title:  "Mark the rows you just invoiced",
					Params: json.RawMessage(`{"account":"default","spreadsheet_id":"REPLACE_WITH_YOUR_SHEET_ID","range":"Jobs","columns":["status","invoiced_on"]}`),
					Notes:  "Read the tab with row numbers on, keep the rows you handled, add the two columns with a calculated column, and connect them in here.",
				},
			},
			RequiresConnections: []core.ConnectionRequirement{
				{Kind: "oauth", Name: "google", Note: "Google OAuth — spreadsheets scope."},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "rows", Label: "Rows", Required: true, MIME: []string{"application/json"}},
				{Port: "spreadsheet_id", Label: "Spreadsheet", MIME: []string{"text/plain"}},
			},
			Outputs: []core.Port{
				{Port: "updated_cells", Label: "Cells changed", MIME: []string{"text/plain"}},
				{Port: "spreadsheet_id", Label: "Spreadsheet", MIME: []string{"text/plain"}},
				{Port: "meta", Label: "Details", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"account":{"type":"string","default":"default"},
					"spreadsheet_id":{"type":"string","format":"google-sheet","title":"Spreadsheet","description":"The spreadsheet to write to."},
					"range":{"type":"string","format":"google-sheet-tab","title":"Tab","default":"Sheet1","description":"The tab the rows came from."},
					"columns":{"type":"array","items":{"type":"string"},"title":"Columns to write","description":"Which columns to change. Leave empty to write every column the rows carry (except the row-number column)."},
					"row_column":{"type":"string","title":"Row-number column","default":"_row","x_advanced":true,"description":"The column holding each row's position in the sheet. Read range adds _row when 'Include row numbers' is on."},
					"value_input_option":{"type":"string","enum":["USER_ENTERED","RAW"],"enumNames":["Interpret like typing (dates, formulas)","Store exactly as given"],"default":"USER_ENTERED","title":"Value format"},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1,"description":"Hard deadline for the request, in milliseconds."}
				},
				"required":["spreadsheet_id"]
			}`),
			// Writing the same values to the same cells again lands the same
			// sheet, so a retry is safe.
			Idempotent:  true,
			RetryPolicy: core.RetryExponentialBackoff,
		},
		Execute: executeUpdateCells,
	})
}

func executeUpdateCells(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	id := resolveSpreadsheetID(job)
	if id == "" {
		return params.Err(job, "bad_param", "'spreadsheet_id' is required"), nil
	}
	in, ok := job.Input["rows"]
	if !ok {
		return params.Err(job, "missing_input", "input port 'rows' is required"), nil
	}
	rows, err := normalizeRows(in.Inline)
	if err != nil {
		return params.Err(job, "bad_input", err.Error()), nil
	}
	if len(rows) == 0 {
		// Nothing to mark is a normal outcome for a "handle what's new" flow.
		return core.Result{JobID: job.ID, Status: core.StatusOK, Output: map[string]core.Ref{
			"updated_cells":  {MIME: "text/plain", Inline: "0"},
			"spreadsheet_id": {MIME: "text/plain", Inline: id},
			"meta":           {MIME: "application/json", Inline: map[string]any{"updated_cells": 0, "rows": 0}},
		}}, nil
	}

	token, err := resolveToken(ctx, job)
	if err != nil {
		return params.Err(job, "auth", err.Error()), nil
	}
	tab := params.StringDefault(job.Params, "range", "Sheet1")
	rowCol := params.StringDefault(job.Params, "row_column", RowNumberColumn)
	timeout := params.IntDefault(job.Params, "timeout_ms", 15000)

	// Which columns to write: the named ones, else everything the rows carry
	// apart from the row-number column.
	want := params.StringSlice(job.Params, "columns")
	if len(want) == 0 {
		want = writableColumns(rows, in.Headers, rowCol)
	}
	if len(want) == 0 {
		return params.Err(job, "bad_param", "no columns to write — name them in 'Columns to write', or send rows that carry the values"), nil
	}

	// Map column name → letter from the sheet's own header row, appending any
	// column the sheet doesn't have yet.
	existing, err := readSheetHeaders(ctx, job, id, tab, token, timeout)
	if err != nil {
		return params.Err(job, "sheets_error", err.Error()), nil
	}
	letter := map[string]string{}
	for i, h := range existing {
		letter[h] = columnLetter(i)
	}
	var newHeaders []string
	for _, c := range want {
		if _, known := letter[c]; known {
			continue
		}
		letter[c] = columnLetter(len(existing) + len(newHeaders))
		newHeaders = append(newHeaders, c)
	}

	type valueRange struct {
		Range  string  `json:"range"`
		Values [][]any `json:"values"`
	}
	data := make([]valueRange, 0, len(rows)*len(want)+1)

	// A brand-new column needs its header written too, or the sheet gains a
	// nameless column that no later read can find.
	for i, h := range newHeaders {
		col := columnLetter(len(existing) + i)
		data = append(data, valueRange{
			Range:  quoteSheetTab(tab) + "!" + col + "1",
			Values: [][]any{{h}},
		})
	}

	for i, row := range rows {
		n, ok := rowNumber(row[rowCol])
		if !ok {
			return params.Err(job, "bad_input", fmt.Sprintf(
				"row %d has no %q — turn on 'Include row numbers' in the Read range step so each row knows where it came from", i+1, rowCol)), nil
		}
		for _, c := range want {
			v, present := row[c]
			if !present {
				continue
			}
			data = append(data, valueRange{
				Range:  quoteSheetTab(tab) + "!" + letter[c] + strconv.Itoa(n),
				Values: [][]any{{v}},
			})
		}
	}
	if len(data) == 0 {
		return params.Err(job, "bad_input", "the rows carry none of the columns to write"), nil
	}

	payload, err := json.Marshal(map[string]any{
		"valueInputOption": params.StringDefault(job.Params, "value_input_option", "USER_ENTERED"),
		"data":             data,
	})
	if err != nil {
		return params.Err(job, "sheets_error", err.Error()), nil
	}
	endpoint := sheetsBaseURL(job) + "/spreadsheets/" + url.PathEscape(id) + "/values:batchUpdate"
	status, body, err := googleDo(ctx, "POST", endpoint, token, "application/json", payload, timeout)
	if err != nil {
		return params.Err(job, "sheets_http_error", err.Error()), nil
	}
	if status < 200 || status >= 300 {
		return params.Err(job, "sheets_error", sheetsErr(body)), nil
	}
	var parsed struct {
		TotalUpdatedCells int `json:"totalUpdatedCells"`
	}
	_ = json.Unmarshal(body, &parsed)

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"updated_cells":  {MIME: "text/plain", Inline: strconv.Itoa(parsed.TotalUpdatedCells)},
			"spreadsheet_id": {MIME: "text/plain", Inline: id},
			"meta": {MIME: "application/json", Inline: map[string]any{
				"updated_cells": parsed.TotalUpdatedCells,
				"rows":          len(rows),
				"columns":       want,
			}},
		},
	}, nil
}

// writableColumns is every column the rows carry except the row-number one,
// in the incoming header order when there is one (so the write follows the
// sheet's own column order rather than map iteration).
func writableColumns(rows []map[string]any, headers []string, rowCol string) []string {
	var out []string
	seen := map[string]bool{}
	for _, h := range headers {
		if h != rowCol && !seen[h] {
			out = append(out, h)
			seen[h] = true
		}
	}
	if len(out) > 0 {
		return out
	}
	for _, row := range rows {
		for k := range row {
			if k != rowCol && !seen[k] {
				out = append(out, k)
				seen[k] = true
			}
		}
	}
	sort.Strings(out)
	return out
}

// rowNumber reads a sheet row position out of whatever the row carries — an
// int from a fresh read, a float64 after a JSON round-trip, or text.
func rowNumber(v any) (int, bool) {
	switch t := v.(type) {
	case int:
		return t, t > 0
	case int64:
		return int(t), t > 0
	case float64:
		return int(t), t > 0
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(t))
		return n, err == nil && n > 0
	}
	return 0, false
}
