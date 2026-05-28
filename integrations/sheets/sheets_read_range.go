package sheets

import (
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
			ID:             "sheets_read_range",
			Version:        "1.0",
			Label:          "Sheets read range",
			Color:          "#0F9D58",
			Icon:           "file-input",
			BrandLogo:      "/brands/sheets.svg",
			Category:       "network",
			Provider:       "internal",
			Integration:    "Google Sheets",
			Tags:           []string{"sheets", "google", "read", "etl"},
			Description:    "Read a range from a Google Sheet into rows. The first row becomes the headers by default (override with headers=false for synthetic col_0/col_1 names). The output drops straight into Excel, Postgres, and the transform drops — same shape across the family.",
			Summary:        "Read a tab or A1 range from a Google Sheet into rows + headers for downstream drops.",
			Examples: []core.ParamsExample{
				{
					Title:  "Read the whole first sheet",
					Params: json.RawMessage(`{"account":"default","spreadsheet_id":"1AbcDEFghIJklmNOPqrsTUVwxyZ_0123456789abcd","range":"Sheet1","headers":true}`),
				},
				{
					Title:  "Read a specific A1 range with raw numbers",
					Params: json.RawMessage(`{"account":"default","spreadsheet_id":"1AbcDEFghIJklmNOPqrsTUVwxyZ_0123456789abcd","range":"orders!A1:F500","value_render_option":"UNFORMATTED_VALUE"}`),
					Notes:  "UNFORMATTED_VALUE keeps numbers as numbers; FORMATTED_VALUE returns Sheets' display strings (handy when a downstream report wants the formatted currency).",
				},
				{
					Title:  "Headerless read with synthetic col_N names",
					Params: json.RawMessage(`{"account":"default","spreadsheet_id":"1AbcDEFghIJklmNOPqrsTUVwxyZ_0123456789abcd","range":"raw_dump!A1:Z","headers":false}`),
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Outputs: []core.Port{
				{Port: "rows", Label: "Rows", MIME: []string{"application/json"}},
				{Port: "headers", Label: "Headers", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"account":              {"type":"string","default":"default"},
					"token":                {"type":"string","description":"Raw access token; overrides 'account'."},
					"spreadsheet_id":       {"type":"string","description":"The Sheet ID from its URL."},
					"range":                {"type":"string","default":"Sheet1","description":"Sheet name (\"Sheet1\") or full range (\"Sheet1!A1:D100\"). Without a sheet name, uses the first sheet."},
					"headers":              {"type":"boolean","default":true,"description":"When true, the first row of the range is treated as column headers."},
					"value_render_option":  {"type":"string","enum":["FORMATTED_VALUE","UNFORMATTED_VALUE","FORMULA"],"default":"FORMATTED_VALUE","description":"FORMATTED_VALUE returns what the user sees in Sheets (currency formatting, date strings); UNFORMATTED_VALUE returns raw numbers; FORMULA returns the formula source for computed cells."},
					"timeout_ms":           {"type":"integer","default":15000,"minimum":1}
				},
				"required":["spreadsheet_id"]
			}`),
			Idempotent: true,
		},
		Execute: executeSheetsReadRange,
	})
}

// executeSheetsReadRange GETs values from the Sheets API and
// flattens the [][]any matrix into the standard rows-and-headers
// shape. Same logic as excel_read's flatten step: short rows
// (Sheets trims trailing blanks) get padded with "" so every output
// map has the full header key set.
//
// Values: with value_render_option=FORMATTED_VALUE (the default),
// Sheets returns strings even for numeric cells — matches what
// excel_read does and means downstream consumers don't have to
// type-juggle between sources. UNFORMATTED_VALUE returns proper
// number/bool types if the downstream code prefers them.
func executeSheetsReadRange(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	spreadsheetID, err := params.String(job.Params, "spreadsheet_id")
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	token, err := resolveToken(ctx, job)
	if err != nil {
		return params.Err(job, "auth", err.Error()), nil
	}

	rangeRef := params.StringDefault(job.Params, "range", "Sheet1")
	q := url.Values{}
	q.Set("valueRenderOption", params.StringDefault(job.Params, "value_render_option", "FORMATTED_VALUE"))
	// Always use ROWS as major dimension — matches every other
	// tabular drop's "row-of-values" mental model.
	q.Set("majorDimension", "ROWS")

	endpoint := fmt.Sprintf("%s/spreadsheets/%s/values/%s?%s",
		currentHTTPBase(),
		url.PathEscape(spreadsheetID),
		url.PathEscape(rangeRef),
		q.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return params.Err(job, "internal", err.Error()), nil
	}
	req.Header.Set("Authorization", "Bearer "+token)

	timeoutMs := params.IntDefault(job.Params, "timeout_ms", 15000)
	client := &http.Client{Timeout: time.Duration(timeoutMs) * time.Millisecond}
	resp, err := client.Do(req)
	if err != nil {
		return params.Err(job, "send_failed", err.Error()), nil
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 5<<20)) // 5 MiB cap; bigger sheets need pagination by range

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return params.Err(job, "sheets_error",
			fmt.Sprintf("Sheets returned %d: %s", resp.StatusCode, extractSheetsError(body))), nil
	}

	var parsed struct {
		Values [][]any `json:"values"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return params.Err(job, "parse", err.Error()), nil
	}

	useHeaders := params.BoolDefault(job.Params, "headers", true)
	headers, rows := flattenSheetsValues(parsed.Values, useHeaders)
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"rows":    {MIME: "application/json", Inline: rows},
			"headers": {MIME: "application/json", Inline: headers},
		},
	}, nil
}

// flattenSheetsValues mirrors integrations/io/excel_read's
// flattenRows shape, but operates on []any cells (Sheets returns
// values as any since FORMATTED_VALUE → string and
// UNFORMATTED_VALUE → number/bool depending on the cell). Headers
// are always stringified — column NAMES are never numeric data.
func flattenSheetsValues(raw [][]any, useHeaders bool) ([]string, []map[string]any) {
	if len(raw) == 0 {
		return []string{}, []map[string]any{}
	}

	var headers []string
	var data [][]any
	if useHeaders {
		first := raw[0]
		headers = make([]string, len(first))
		for i, v := range first {
			headers[i] = stringifyCell(v)
		}
		data = raw[1:]
	} else {
		// Find the widest row so synthetic col_N headers cover
		// everything Sheets returned.
		maxCols := 0
		for _, r := range raw {
			if len(r) > maxCols {
				maxCols = len(r)
			}
		}
		headers = make([]string, maxCols)
		for i := range headers {
			headers[i] = fmt.Sprintf("col_%d", i)
		}
		data = raw
	}

	rows := make([]map[string]any, 0, len(data))
	for _, r := range data {
		rec := make(map[string]any, len(headers))
		for i, h := range headers {
			if i < len(r) {
				rec[h] = r[i]
			} else {
				// Sheets trims trailing blank cells from each row —
				// pad with "" so the row map always has the full
				// header key set.
				rec[h] = ""
			}
		}
		rows = append(rows, rec)
	}
	return headers, rows
}

// stringifyCell coerces a header cell value to its display string.
// Most header rows are text already, but a user with numeric
// headers (e.g. fiscal-year columns "2024", "2025") deserves
// reasonable behavior — fmt.Sprint covers the common cases.
func stringifyCell(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}
