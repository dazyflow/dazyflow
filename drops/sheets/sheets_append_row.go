// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package sheets

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
	"git.sr.ht/~klahr/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "sheets_append_row",
			Version:     "1.0",
			Label:       "Google Sheets",
			Subtitle:    "Append rows",
			Summary:     "Append rows to a Google Sheet, matching each object's fields to columns by header.",
			Description: "Append rows to a Google Sheet. Wire a rows list into the 'rows' input; columns are taken from the 'headers' input or derived from the row keys. Each object becomes a row. Set a 'mapping' to pick which incoming field fills which sheet column (e.g. a Google Form response's question titles → your sheet's columns) — both sides are chosen from dropdowns (the upstream record's fields and the sheet's own columns), and the mapping's columns then define the row, in order.",
			Integration: "Google Sheets",
			Category:    "network",
			Icon:        "file-output",
			BrandLogo:   "/brands/sheets.svg",
			Color:       "#0F9D58",
			Provider:    "internal",
			Tags:        []string{"sheets", "google", "append", "write"},
			Examples: []core.ParamsExample{
				{Title: "Append to a log sheet", Params: json.RawMessage(`{"account":"default","spreadsheet_id":"REPLACE_WITH_YOUR_SHEET_URL_OR_ID","range":"Inbox Log"}`)},
			},
			RequiresConnections: []core.ConnectionRequirement{
				{Kind: "oauth", Name: "google", Note: "Google OAuth — sheets/drive scope."},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "rows", Label: "Rows", Required: true, MIME: []string{"application/json"}},
				// Optional: wire a spreadsheet id in to override the picker, so a
				// reference can be threaded from an upstream sheet step.
				{Port: "spreadsheet_id", Label: "Spreadsheet ID", MIME: []string{"text/plain"}},
			},
			Outputs: []core.Port{
				// Only the friendly scalars are declared as ports; the full
				// structured result is still EMITTED under "meta" (see
				// appendOutputs) so run records keep it for debugging, but it's
				// not a pin — undeclared outputs can't be wired and don't
				// clutter the card. spreadsheet_id is emitted so it can feed
				// another sheet step's 'spreadsheet_id' input.
				{Port: "appended_rows", Label: "Rows saved", MIME: []string{"text/plain"}},
				{Port: "updated_range", Label: "Updated range", MIME: []string{"text/plain"}},
				{Port: "spreadsheet_id", Label: "Spreadsheet ID", MIME: []string{"text/plain"}},
				{Port: "meta", Label: "Details", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"account":{"type":"string","default":"default"},
					"spreadsheet_id":{"type":"string","format":"google-spreadsheet","title":"Spreadsheet","description":"The spreadsheet to append to."},
					"range":{"type":"string","format":"google-sheet-tab","title":"Tab","default":"Sheet1","description":"The sheet tab the append targets."},
					"value_input_option":{"type":"string","title":"Value format","enum":["RAW","USER_ENTERED"],"enumNames":["Raw values","Like typing in Sheets"],"default":"USER_ENTERED"},
					"insert_data_option":{"type":"string","title":"Adding rows","enum":["OVERWRITE","INSERT_ROWS"],"enumNames":["Overwrite existing cells","Insert new rows"],"default":"INSERT_ROWS"},
					"mapping":{
						"type":"array",
						"title":"Column mapping",
						"format":"sheet-mapping",
						"description":"Map each incoming field to a sheet column. Both sides are picked from dropdowns — the sheet's own columns and the upstream record's fields. Each value is written under the column you choose (the row order here doesn't matter); a column the sheet doesn't have yet is added at the end. When set, this replaces the 'headers' input. Leave empty to use the row keys / 'headers' input.",
						"items":{
							"type":"object",
							"properties":{
								"column":{"type":"string","title":"Sheet column"},
								"source":{"type":"string","title":"From field"}
							}
						}
					},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1,"description":"Hard deadline for the request, in milliseconds."}
				},
				"required":["spreadsheet_id"]
			}`),
			Idempotent: false,
			// Sheets values.append has no idempotency header, so a retried
			// POST appends the row twice. This drop is a terminal leaf the
			// engine auto-retries on backoff, so retries must be off here.
			RetryPolicy: core.RetryNever,
			// …and the engine dedupes a same-job re-execution (expired-lease
			// reclaim / crash recovery) so a recovered run doesn't re-append.
			DedupeWrites: true,
		},
		Execute: executeSheetsAppend,
	})
}

func executeSheetsAppend(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	id := resolveSpreadsheetID(job)
	if id == "" {
		return params.Err(job, "bad_param", "'spreadsheet_id' is required"), nil
	}
	token, err := resolveToken(ctx, job)
	if err != nil {
		return params.Err(job, "auth", err.Error()), nil
	}

	in, ok := job.Input["rows"]
	if !ok {
		return params.Err(job, "missing_input", "input port 'rows' is required"), nil
	}
	rows, err := normalizeRows(in.Inline)
	if err != nil {
		return params.Err(job, "bad_input", err.Error()), nil
	}

	rng := params.StringDefault(job.Params, "range", "Sheet1")
	timeout := params.IntDefault(job.Params, "timeout_ms", 15000)

	var headers []string
	// Prefer the column order folded onto the rows value itself. (A 'mapping'
	// param still overrides it, same as before — see the mapping block below.)
	if len(in.Headers) > 0 {
		headers = in.Headers
	}

	// An explicit column mapping is placed BY COLUMN NAME, not by row order:
	// we read the sheet's existing header row and position each mapped value
	// under its named column. So the column you map to decides where the value
	// lands, and the order of the mapping rows is irrelevant. Any mapped column
	// the sheet doesn't have yet is added as a new trailing column (its header
	// is written below before the append). Without a mapping we keep the old
	// behaviour: the 'headers' input or row keys, placed in order.
	var writeHeaderCols []string // set when a new column must be (re)written to row 1
	if cmap := parseMapping(job.Params); len(cmap) > 0 {
		existing, herr := readSheetHeaders(ctx, job, id, rng, token, timeout)
		if herr != nil {
			return params.Err(job, "sheets_error", herr.Error()), nil
		}
		cols := append([]string{}, existing...)
		seen := map[string]bool{}
		for _, c := range existing {
			seen[c] = true
		}
		for _, c := range mappingColumns(cmap) {
			if c != "" && !seen[c] {
				cols = append(cols, c)
				seen[c] = true
				writeHeaderCols = cols // a new column → row 1 needs (re)writing
			}
		}
		headers = cols
		rows = projectRows(rows, cmap)
	}

	if headers == nil {
		headers = deriveHeaders(rows)
	}

	values := make([][]any, 0, len(rows))
	for _, row := range rows {
		rec := make([]any, len(headers))
		for i, h := range headers {
			if v, ok := row[h]; ok {
				rec[i] = cellValue(v)
			} else {
				rec[i] = ""
			}
		}
		values = append(values, rec)
	}
	if len(values) == 0 {
		// Nothing to write (empty rows / all-filtered). Still emit every port
		// — with zero counts and an empty range — so downstream wiring never
		// sees a missing port. The url still points at the target sheet.
		return core.Result{
			JobID:  job.ID,
			Status: core.StatusOK,
			Output: appendOutputs(id, "", 0, map[string]any{
				"appended_rows":   0,
				"spreadsheet_id":  id,
				"updated_range":   "",
				"updated_rows":    0,
				"updated_columns": 0,
				"updated_cells":   0,
			}),
		}, nil
	}

	// Create any new mapped columns by (re)writing the header row first, so the
	// appended values line up with their named columns. RAW so the header text
	// is stored verbatim (no formula/date parsing).
	if len(writeHeaderCols) > 0 {
		hdr := make([]any, len(writeHeaderCols))
		for i, c := range writeHeaderCols {
			hdr[i] = c
		}
		hdrRange := quoteSheetTab(rng) + "!A1"
		hq := url.Values{}
		hq.Set("valueInputOption", "RAW")
		hdrEndpoint := sheetsBaseURL(job) + "/spreadsheets/" + url.PathEscape(id) + "/values/" + url.PathEscape(hdrRange) + "?" + hq.Encode()
		hdrBody, _ := json.Marshal(map[string]any{"range": hdrRange, "majorDimension": "ROWS", "values": [][]any{hdr}})
		hstatus, hbody, herr := googleDo(ctx, "PUT", hdrEndpoint, token, "application/json; charset=utf-8", hdrBody, timeout)
		if herr != nil {
			return params.Err(job, "sheets_http_error", herr.Error()), nil
		}
		if hstatus < 200 || hstatus >= 300 {
			return params.Err(job, "sheets_error", sheetsErr(hbody)), nil
		}
	}

	q := url.Values{}
	q.Set("valueInputOption", params.StringDefault(job.Params, "value_input_option", "USER_ENTERED"))
	q.Set("insertDataOption", params.StringDefault(job.Params, "insert_data_option", "INSERT_ROWS"))
	endpoint := sheetsBaseURL(job) + "/spreadsheets/" + url.PathEscape(id) + "/values/" + url.PathEscape(rng) + ":append?" + q.Encode()

	reqBody, _ := json.Marshal(map[string]any{"range": rng, "majorDimension": "ROWS", "values": values})
	status, body, err := googleDo(ctx, "POST", endpoint, token, "application/json; charset=utf-8", reqBody, timeout)
	if err != nil {
		return params.Err(job, "sheets_http_error", err.Error()), nil
	}
	if status < 200 || status >= 300 {
		return params.Err(job, "sheets_error", sheetsErr(body)), nil
	}

	var parsed struct {
		Updates struct {
			UpdatedRange   string `json:"updatedRange"`
			UpdatedRows    int    `json:"updatedRows"`
			UpdatedColumns int    `json:"updatedColumns"`
			UpdatedCells   int    `json:"updatedCells"`
		} `json:"updates"`
	}
	_ = json.Unmarshal(body, &parsed)
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: appendOutputs(id, parsed.Updates.UpdatedRange, len(values), map[string]any{
			"appended_rows":   len(values),
			"spreadsheet_id":  id,
			"updated_range":   parsed.Updates.UpdatedRange,
			"updated_rows":    parsed.Updates.UpdatedRows,
			"updated_columns": parsed.Updates.UpdatedColumns,
			"updated_cells":   parsed.Updates.UpdatedCells,
		}),
	}, nil
}

// cellValue makes any row field safe to send as a Sheets cell. The API only
// accepts scalars (string/number/bool/null); a nested object or list — e.g.
// a row coming from for_each's Failed rows, whose entries carry a structured
// `data`/`error` — would fail the WHOLE append, so those are written as their
// JSON text instead. Scalars pass through untouched (Sheets keeps numbers as
// numbers).
func cellValue(v any) any {
	switch v.(type) {
	case nil, string, bool,
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64, json.Number:
		return v
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprintf("%v", v)
		}
		return string(b)
	}
}

// appendOutputs bundles the drop's four output ports: the full structured
// `meta` (json) plus the scalar `appended_rows`, `updated_range`, and
// `spreadsheet_id` (text) so common downstream steps can wire them without a
// JSON extract — notably `spreadsheet_id` into another sheet step's input.
// Used by both the success path and the empty (zero-row) path so the port
// set is identical either way.
func appendOutputs(id, updatedRange string, count int, meta map[string]any) map[string]core.Ref {
	return map[string]core.Ref{
		"meta":           {MIME: "application/json", Inline: meta},
		"appended_rows":  {MIME: "text/plain", Inline: strconv.Itoa(count)},
		"updated_range":  {MIME: "text/plain", Inline: updatedRange},
		"spreadsheet_id": {MIME: "text/plain", Inline: id},
	}
}
