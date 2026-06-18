package sheets

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
	"git.sr.ht/~klahr/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "sheets_read_range",
			Version:     "1.0",
			Label:       "Google Sheets",
			Subtitle:    "Read range",
			Summary:     "Read a range from a Google Sheet into rows + headers.",
			Description: "Read a range of a Google Sheet. The first row becomes the column headers (unless headers=false), and each subsequent row becomes an object keyed by header. Paste a sheet URL or its ID.",
			Integration: "Google Sheets",
			Category:    "network",
			Icon:        "file-input",
			BrandLogo:   "/brands/sheets.svg",
			Color:       "#0F9D58",
			Provider:    "internal",
			Tags:        []string{"sheets", "google", "read", "spreadsheet"},
			Examples: []core.ParamsExample{
				{Title: "Read a named range", Params: json.RawMessage(`{"account":"default","spreadsheet_id":"REPLACE_WITH_YOUR_SHEET_ID","range":"Master List"}`)},
			},
			RequiresConnections: []core.ConnectionRequirement{
				{Kind: "oauth", Name: "google", Note: "Google OAuth — sheets/drive scope."},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				// Optional: wire a spreadsheet id in to override the picker, so a
				// reference can be threaded from an upstream sheet step (e.g.
				// append row's 'spreadsheet_id' output).
				{Port: "spreadsheet_id", Label: "Spreadsheet ID", MIME: []string{"text/plain"}},
			},
			Outputs: []core.Port{
				{Port: "rows", Label: "Rows", MIME: []string{"application/json"}},
				{Port: "headers", Label: "Headers", MIME: []string{"application/json"}},
				// spreadsheet_id is re-emitted (same as append's) so any sheet
				// step downstream can target the same spreadsheet by wire.
				{Port: "spreadsheet_id", Label: "Spreadsheet ID", MIME: []string{"text/plain"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"account":{"type":"string","default":"default"},
					"spreadsheet_id":{"type":"string","format":"google-spreadsheet","title":"Spreadsheet","description":"The spreadsheet to read."},
					"range":{"type":"string","format":"google-sheet-tab","title":"Tab","default":"Sheet1","description":"The sheet tab or named range to read."},
						"cells":{"type":"string","title":"Cells","examples":["A1:D5"],"description":"Optional cell range within the tab (A1 notation). Leave blank to read the whole tab."},
					"headers":{"type":"boolean","title":"First row is headers","default":true,"description":"Treat the first row as column headers."},
					"value_render_option":{"type":"string","title":"Value format","enum":["FORMATTED_VALUE","UNFORMATTED_VALUE","FORMULA"],"enumNames":["As displayed","Raw values","Formulas"],"default":"FORMATTED_VALUE"},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1,"description":"Hard deadline for the request, in milliseconds."}
				},
				"required":["spreadsheet_id"]
			}`),
			Idempotent: true,
		},
		Execute: executeSheetsRead,
	})
}

func executeSheetsRead(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	id := resolveSpreadsheetID(job)
	if id == "" {
		return params.Err(job, "bad_param", "'spreadsheet_id' is required"), nil
	}
	headers, rows, err := ReadRange(ctx, job)
	if err != nil {
		return params.Err(job, "sheets_error", err.Error()), nil
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"rows":           {MIME: "application/json", Inline: rows},
			"headers":        {MIME: "application/json", Inline: headers},
			"spreadsheet_id": {MIME: "text/plain", Inline: id},
		},
	}, nil
}

// ReadRange fetches a sheet range and flattens it to ordered headers + row
// objects — the exact read the sheets_read_range node performs, exported so
// the daemon's resource provider (${resource.NAME} of type google_sheet)
// can reuse it instead of reimplementing the Google call. Reads
// spreadsheet_id, range, account, value_render_option, headers and
// timeout_ms from job.Params; resolves the Google token via the package's
// SetTokenLookup hook (tenant rides on ctx).
func ReadRange(ctx context.Context, job core.Job) (headers []string, rows []map[string]any, err error) {
	id := resolveSpreadsheetID(job)
	if id == "" {
		return nil, nil, fmt.Errorf("'spreadsheet_id' is required")
	}
	token, err := resolveToken(ctx, job)
	if err != nil {
		return nil, nil, err
	}
	rng := params.StringDefault(job.Params, "range", "Sheet1")
	// An optional cell range narrows the read to a block within the tab
	// (A1 notation). Quote the tab so names with spaces parse, e.g.
	// 'Inbox Log'!A1:D5. Blank → read the whole tab/named range as before.
	if cells := strings.TrimSpace(params.StringDefault(job.Params, "cells", "")); cells != "" {
		rng = quoteSheetTab(rng) + "!" + cells
	}

	q := url.Values{}
	q.Set("valueRenderOption", params.StringDefault(job.Params, "value_render_option", "FORMATTED_VALUE"))
	q.Set("majorDimension", "ROWS")
	endpoint := sheetsBaseURL(job) + "/spreadsheets/" + url.PathEscape(id) + "/values/" + url.PathEscape(rng) + "?" + q.Encode()

	status, body, err := googleDo(ctx, "GET", endpoint, token, "", nil, params.IntDefault(job.Params, "timeout_ms", 15000))
	if err != nil {
		return nil, nil, err
	}
	if status < 200 || status >= 300 {
		return nil, nil, fmt.Errorf("%s", sheetsErr(body))
	}

	var parsed struct {
		Values [][]any `json:"values"`
	}
	_ = json.Unmarshal(body, &parsed)
	headers, rows = flattenValues(parsed.Values, params.BoolDefault(job.Params, "headers", true))
	return headers, rows, nil
}
