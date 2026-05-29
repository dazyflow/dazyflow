package sheets

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/engine"
	"git.sr.ht/~klahr/hazy-flow/integrations/internal/params"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:             "sheets_append_row",
			Version:        "1.0",
			Label:          "Sheets append rows",
			Color:          "#0F9D58",
			Icon:           "file-output",
			BrandLogo:      "/brands/sheets.svg",
			Category:       "network",
			Provider:       "internal",
			Integration:    "Google Sheets",
			Tags:           []string{"sheets", "google", "append", "log", "etl"},
			Description:    "Append rows to a Google Sheet. Useful for logging events into a spreadsheet teammates can inspect, or for keeping a Sheet in sync with another source. By default Sheets parses '30' as a number and '=SUM(A:A)' as a formula — switch to RAW mode if you want everything stored as literal strings.",
			Summary:        "Append rows to a Google Sheet tab, with the first row picking column order from headers.",
			Examples: []core.ParamsExample{
				{
					Title:  "Append to the default sheet",
					Params: json.RawMessage(`{"account":"default","spreadsheet_id":"1AbcDEFghIJklmNOPqrsTUVwxyZ_0123456789abcd","range":"Sheet1"}`),
					Notes:  "Rows come in on the 'rows' input port; the column order is derived from row keys (alphabetical) when no headers input is wired.",
				},
				{
					Title:  "Append to a named tab with RAW value mode",
					Params: json.RawMessage(`{"account":"default","spreadsheet_id":"1AbcDEFghIJklmNOPqrsTUVwxyZ_0123456789abcd","range":"event_log!A:E","value_input_option":"RAW","insert_data_option":"INSERT_ROWS"}`),
					Notes:  "RAW preserves leading zeros and stops Sheets from interpreting '=SUM(...)' as a formula.",
				},
				{
					Title:  "Append using a raw access token",
					Params: json.RawMessage(`{"token":"${secret:SHEETS_OAUTH}","spreadsheet_id":"1AbcDEFghIJklmNOPqrsTUVwxyZ_0123456789abcd","range":"Sheet1","timeout_ms":20000}`),
				},
			},
			RequiresConnections: []core.ConnectionRequirement{
				{Kind: "oauth", Name: "sheets", Note: "Google Sheets OAuth — drive.file scope."},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "rows", Label: "Rows", Required: true, MIME: []string{"application/json"}},
				{Port: "headers", Label: "Headers (column order)", Required: false, MIME: []string{"application/json"}},
			},
			Outputs: []core.Port{
				{Port: "meta", Label: "Append metadata", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"account":            {"type":"string","default":"default"},
					"token":              {"type":"string","description":"Raw access token; overrides 'account'."},
					"spreadsheet_id":     {"type":"string","description":"Either the Sheet ID (the long string between /d/ and /edit in the URL) or paste the whole URL — the drop extracts the ID for you."},
					"range":              {"type":"string","default":"Sheet1","description":"Sheet name (e.g. \"Sheet1\") or a full range (\"Sheet1!A1:Z\"). Sheets appends after the last populated row in this range."},
					"value_input_option": {"type":"string","enum":["USER_ENTERED","RAW"],"default":"USER_ENTERED","description":"USER_ENTERED parses strings as numbers/dates/formulas; RAW keeps them literal."},
					"insert_data_option": {"type":"string","enum":["INSERT_ROWS","OVERWRITE"],"default":"INSERT_ROWS","description":"INSERT_ROWS pushes existing rows down; OVERWRITE fills empty cells in place."},
					"timeout_ms":         {"type":"integer","default":15000,"minimum":1}
				},
				"required":["spreadsheet_id"]
			}`),
			Idempotent:  false, // Repeating an append duplicates rows.
			RetryPolicy: core.RetryExponentialBackoff,
		},
		Execute: executeSheetsAppendRow,
	})
}

// executeSheetsAppendRow converts the canonical {column: value} rows
// shape into Google's `[[value, ...], ...]` lists-of-lists format,
// then POSTs to the values.append endpoint. Column order comes from
// the optional `headers` input or, when absent, is derived from row
// keys (sorted alphabetically — same rule as the SQL drops).
//
// Cell value handling: pass Go-native values through to JSON. Sheets
// with value_input_option=USER_ENTERED interprets strings (parsing
// dates, numbers, formulas); ints/floats/bools become typed cells
// directly. The result matches what a human typing the value would
// see — surprising in a few edge cases (a string "01234" becomes the
// number 1234, dropping the leading zero) but the right default for
// ETL into a human-edited sheet. Pass value_input_option=RAW to
// preserve literal strings.
func executeSheetsAppendRow(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	spreadsheetIDRaw, err := params.String(job.Params, "spreadsheet_id")
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	spreadsheetID := normalizeSpreadsheetID(spreadsheetIDRaw)
	token, err := resolveToken(ctx, job)
	if err != nil {
		return params.Err(job, "auth", err.Error()), nil
	}

	rowsRef, ok := job.Input["rows"]
	if !ok {
		return params.Err(job, "missing_input", "input port 'rows' is required"), nil
	}
	rows, err := normalizeRows(rowsRef.Inline)
	if err != nil {
		return params.Err(job, "bad_input", err.Error()), nil
	}

	var headers []string
	if h, ok := job.Input["headers"]; ok && h.Inline != nil {
		headers, err = normalizeHeaders(h.Inline)
		if err != nil {
			return params.Err(job, "bad_input", err.Error()), nil
		}
	}
	if headers == nil {
		headers = deriveHeaders(rows)
	}

	// Build the values matrix. Sheets wants [][]any with the outer
	// slice being rows and the inner being cells in column order.
	values := make([][]any, 0, len(rows))
	for _, row := range rows {
		cells := make([]any, len(headers))
		for i, h := range headers {
			if v, ok := row[h]; ok {
				cells[i] = v
			} else {
				cells[i] = ""
			}
		}
		values = append(values, cells)
	}

	if len(values) == 0 {
		// Sheets returns 400 for an empty values list. Short-circuit
		// with a clean OK + zero count rather than confusing the user
		// with a 400 about an empty operation they probably intended.
		return core.Result{
			JobID:  job.ID,
			Status: core.StatusOK,
			Output: map[string]core.Ref{
				"meta": {MIME: "application/json", Inline: map[string]any{
					"appended_rows": 0,
				}},
			},
		}, nil
	}

	rangeRef := params.StringDefault(job.Params, "range", "Sheet1")
	payload := map[string]any{
		"range":          rangeRef,
		"majorDimension": "ROWS",
		"values":         values,
	}
	body, _ := json.Marshal(payload)

	q := url.Values{}
	q.Set("valueInputOption", params.StringDefault(job.Params, "value_input_option", "USER_ENTERED"))
	q.Set("insertDataOption", params.StringDefault(job.Params, "insert_data_option", "INSERT_ROWS"))

	endpoint := fmt.Sprintf("%s/spreadsheets/%s/values/%s:append?%s",
		currentHTTPBase(),
		url.PathEscape(spreadsheetID),
		url.PathEscape(rangeRef),
		q.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return params.Err(job, "internal", err.Error()), nil
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	timeoutMs := params.IntDefault(job.Params, "timeout_ms", 15000)
	client := &http.Client{Timeout: time.Duration(timeoutMs) * time.Millisecond}
	resp, err := client.Do(req)
	if err != nil {
		return params.Err(job, "send_failed", err.Error()), nil
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return params.Err(job, "sheets_error",
			fmt.Sprintf("Sheets returned %d: %s", resp.StatusCode, extractSheetsError(respBody))), nil
	}

	var parsed map[string]any
	_ = json.Unmarshal(respBody, &parsed)
	// Sheets' append response wraps update info under `updates`.
	// updatedRange tells you exactly where the rows landed
	// ("Sheet1!A12:C13") — useful for follow-up writes to the same
	// rows or for a Slack notification ("appended to row 12-13").
	updates, _ := parsed["updates"].(map[string]any)
	meta := map[string]any{
		"appended_rows":   len(values),
		"spreadsheet_id":  spreadsheetID,
		"updated_range":   stringField(updates, "updatedRange"),
		"updated_rows":    intField(updates, "updatedRows"),
		"updated_columns": intField(updates, "updatedColumns"),
		"updated_cells":   intField(updates, "updatedCells"),
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"meta": {MIME: "application/json", Inline: meta},
		},
	}, nil
}

func stringField(m map[string]any, k string) string {
	if m == nil {
		return ""
	}
	if s, ok := m[k].(string); ok {
		return s
	}
	return ""
}

func intField(m map[string]any, k string) int {
	if m == nil {
		return 0
	}
	switch v := m[k].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	}
	return 0
}
