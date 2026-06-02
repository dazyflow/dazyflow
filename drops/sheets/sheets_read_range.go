package sheets

import (
	"context"
	"encoding/json"
	"net/url"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/drops/internal/params"
	"git.sr.ht/~klahr/hazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "sheets_read_range",
			Version:     "1.0",
			Label:       "Sheets read range",
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
			Outputs: []core.Port{
				{Port: "rows", Label: "Rows", MIME: []string{"application/json"}},
				{Port: "headers", Label: "Headers", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"base_url":{"type":"string","description":"Override the API host (testing)."},
					"account":{"type":"string","default":"default"},
					"token":{"type":"string","description":"Raw access token; overrides 'account'."},
					"spreadsheet_id":{"type":"string","description":"Spreadsheet ID or full URL."},
					"range":{"type":"string","default":"Sheet1","description":"A1 range or sheet/named range."},
					"headers":{"type":"boolean","default":true,"description":"Treat the first row as column headers."},
					"value_render_option":{"type":"string","enum":["FORMATTED_VALUE","UNFORMATTED_VALUE","FORMULA"],"default":"FORMATTED_VALUE"},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1}
				},
				"required":["spreadsheet_id"]
			}`),
			Idempotent: true,
		},
		Execute: executeSheetsRead,
	})
}

func executeSheetsRead(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	id := sheetID(params.StringDefault(job.Params, "spreadsheet_id", ""))
	if id == "" {
		return params.Err(job, "bad_param", "'spreadsheet_id' is required"), nil
	}
	token, err := resolveToken(ctx, job)
	if err != nil {
		return params.Err(job, "auth", err.Error()), nil
	}
	rng := params.StringDefault(job.Params, "range", "Sheet1")

	q := url.Values{}
	q.Set("valueRenderOption", params.StringDefault(job.Params, "value_render_option", "FORMATTED_VALUE"))
	q.Set("majorDimension", "ROWS")
	endpoint := sheetsBaseURL(job) + "/spreadsheets/" + url.PathEscape(id) + "/values/" + url.PathEscape(rng) + "?" + q.Encode()

	status, body, err := googleDo(ctx, "GET", endpoint, token, "", nil, params.IntDefault(job.Params, "timeout_ms", 15000))
	if err != nil {
		return params.Err(job, "sheets_http_error", err.Error()), nil
	}
	if status < 200 || status >= 300 {
		return params.Err(job, "sheets_error", sheetsErr(body)), nil
	}

	var parsed struct {
		Values [][]any `json:"values"`
	}
	_ = json.Unmarshal(body, &parsed)
	headers, rows := flattenValues(parsed.Values, params.BoolDefault(job.Params, "headers", true))
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"rows":    {MIME: "application/json", Inline: rows},
			"headers": {MIME: "application/json", Inline: headers},
		},
	}, nil
}
