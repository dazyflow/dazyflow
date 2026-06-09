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
			ID:          "sheets_append_row",
			Version:     "1.0",
			Label:       "Sheets append row",
			Summary:     "Append rows to a Google Sheet, mapping each object to columns by header.",
			Description: "Append rows to a Google Sheet. Wire a rows list into the 'rows' input; columns are taken from the 'headers' input or derived from the row keys. Each object becomes a row. Set a 'mapping' to pick exactly which incoming field lands in which sheet column (e.g. a Google Form response's question titles → your sheet's columns) — the mapping's columns then define the row, in order.",
			Integration: "Google Sheets",
			Category:    "network",
			Icon:        "file-output",
			BrandLogo:   "/brands/sheets.svg",
			Color:       "#0F9D58",
			Provider:    "internal",
			Tags:        []string{"sheets", "google", "append", "write"},
			Examples: []core.ParamsExample{
				{Title: "Append to a log sheet", Params: json.RawMessage(`{"account":"default","spreadsheet_id":"REPLACE_WITH_YOUR_SHEET_ID","range":"Inbox Log"}`)},
			},
			RequiresConnections: []core.ConnectionRequirement{
				{Kind: "oauth", Name: "google", Note: "Google OAuth — sheets/drive scope."},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "rows", Label: "Rows", Required: true, MIME: []string{"application/json"}},
				{Port: "headers", Label: "Headers (column order)", MIME: []string{"application/json"}},
			},
			Outputs: []core.Port{
				{Port: "meta", Label: "Append metadata", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"base_url":{"type":"string","description":"Override the API host (testing)."},
					"account":{"type":"string","default":"default"},
					"token":{"type":"string","description":"Raw access token; overrides 'account'."},
					"spreadsheet_id":{"type":"string","format":"google-spreadsheet","description":"Spreadsheet ID or full URL."},
					"range":{"type":"string","format":"google-sheet-tab","default":"Sheet1","description":"Sheet/range the append targets."},
					"value_input_option":{"type":"string","enum":["RAW","USER_ENTERED"],"default":"USER_ENTERED"},
					"insert_data_option":{"type":"string","enum":["OVERWRITE","INSERT_ROWS"],"default":"INSERT_ROWS"},
					"mapping":{
						"type":"array",
						"title":"Column mapping",
						"format":"sheet-mapping",
						"description":"Map each incoming field to a sheet column. When set, these columns (in order) define the appended row and the 'headers' input is ignored. Leave empty to use the row keys / 'headers' input.",
						"items":{
							"type":"object",
							"properties":{
								"column":{"type":"string","title":"Sheet column"},
								"source":{"type":"string","title":"From field"}
							}
						}
					},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1}
				},
				"required":["spreadsheet_id"]
			}`),
			Idempotent:  false,
			RetryPolicy: core.RetryExponentialBackoff,
		},
		Execute: executeSheetsAppend,
	})
}

func executeSheetsAppend(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	id := sheetID(params.StringDefault(job.Params, "spreadsheet_id", ""))
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

	var headers []string
	if h, ok := job.Input["headers"]; ok && h.Inline != nil {
		headers = normalizeHeaders(h.Inline)
	}

	// An explicit column mapping wins over both the 'headers' input and
	// key-derivation: the mapping's columns (in order) become the row, and
	// each incoming row is projected field→column. This is the "Google Form
	// response → sheet column" path — see parseMapping/projectRows.
	if cmap := parseMapping(job.Params); len(cmap) > 0 {
		headers = mappingColumns(cmap)
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
				rec[i] = v
			} else {
				rec[i] = ""
			}
		}
		values = append(values, rec)
	}
	if len(values) == 0 {
		return core.Result{
			JobID:  job.ID,
			Status: core.StatusOK,
			Output: map[string]core.Ref{"meta": {MIME: "application/json", Inline: map[string]any{"appended_rows": 0}}},
		}, nil
	}

	rng := params.StringDefault(job.Params, "range", "Sheet1")
	q := url.Values{}
	q.Set("valueInputOption", params.StringDefault(job.Params, "value_input_option", "USER_ENTERED"))
	q.Set("insertDataOption", params.StringDefault(job.Params, "insert_data_option", "INSERT_ROWS"))
	endpoint := sheetsBaseURL(job) + "/spreadsheets/" + url.PathEscape(id) + "/values/" + url.PathEscape(rng) + ":append?" + q.Encode()

	reqBody, _ := json.Marshal(map[string]any{"range": rng, "majorDimension": "ROWS", "values": values})
	status, body, err := googleDo(ctx, "POST", endpoint, token, "application/json; charset=utf-8", reqBody, params.IntDefault(job.Params, "timeout_ms", 15000))
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
		Output: map[string]core.Ref{"meta": {MIME: "application/json", Inline: map[string]any{
			"appended_rows":   len(values),
			"spreadsheet_id":  id,
			"updated_range":   parsed.Updates.UpdatedRange,
			"updated_rows":    parsed.Updates.UpdatedRows,
			"updated_columns": parsed.Updates.UpdatedColumns,
			"updated_cells":   parsed.Updates.UpdatedCells,
		}}},
	}, nil
}
