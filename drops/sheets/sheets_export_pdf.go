package sheets

import (
	"context"
	"encoding/json"
	"net/url"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/drops/internal/params"
	"git.sr.ht/~klahr/hazyflow/drops/internal/sandbox"
	"git.sr.ht/~klahr/hazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "sheets_export_pdf",
			Version:     "1.0",
			Label:       "Sheets export PDF",
			Summary:     "Export a Google Sheet as a PDF into the run's sandbox.",
			Description: "Render a Google Sheet to PDF via the Drive export API and write it into the run's scratch sandbox. Wire the 'pdf' output into a file-consuming node (e.g. gmail_send_email's attachments). Paste a sheet URL or its ID.",
			Integration: "Google Sheets",
			Category:    "network",
			Icon:        "file-output",
			BrandLogo:   "/brands/sheets.svg",
			Color:       "#0F9D58",
			Provider:    "internal",
			Tags:        []string{"sheets", "google", "pdf", "export"},
			Examples: []core.ParamsExample{
				{Title: "Export a sheet to a daily PDF", Params: json.RawMessage(`{"account":"default","spreadsheet_id":"REPLACE_WITH_YOUR_SHEET_URL_OR_ID"}`)},
			},
			RequiresConnections: []core.ConnectionRequirement{
				{Kind: "oauth", Name: "google", Note: "Google OAuth — drive.readonly scope."},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Outputs: []core.Port{
				{Port: "pdf", Label: "The exported PDF (file ref)", MIME: []string{"application/pdf"}},
				{Port: "meta", Label: "Export metadata", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"base_url":{"type":"string","description":"Override the API host (testing)."},
					"account":{"type":"string","default":"default"},
					"token":{"type":"string","description":"Raw access token; overrides 'account'."},
					"spreadsheet_id":{"type":"string","description":"Spreadsheet ID or full URL."},
					"path":{"type":"string","description":"Sandbox path to write to. Defaults to scratch://sheet-<id>.pdf."},
					"timeout_ms":{"type":"integer","default":30000,"minimum":1}
				},
				"required":["spreadsheet_id"]
			}`),
			Idempotent: true,
		},
		Execute: executeSheetsExportPDF,
	})
}

func executeSheetsExportPDF(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	id := sheetID(params.StringDefault(job.Params, "spreadsheet_id", ""))
	if id == "" {
		return params.Err(job, "bad_param", "'spreadsheet_id' is required"), nil
	}
	token, err := resolveToken(ctx, job)
	if err != nil {
		return params.Err(job, "auth", err.Error()), nil
	}
	dest := params.StringDefault(job.Params, "path", sandbox.Scheme+"sheet-"+id+".pdf")

	q := url.Values{}
	q.Set("mimeType", "application/pdf")
	endpoint := driveBaseURL(job) + "/files/" + url.PathEscape(id) + "/export?" + q.Encode()
	status, body, err := googleDo(ctx, "GET", endpoint, token, "", nil, params.IntDefault(job.Params, "timeout_ms", 30000))
	if err != nil {
		return params.Err(job, "sheets_http_error", err.Error()), nil
	}
	if status < 200 || status >= 300 {
		return params.Err(job, "sheets_error", "Drive export returned: "+sheetsErr(body)), nil
	}

	root, rel, err := sandbox.OpenRoot(job, dest)
	if err != nil {
		return params.Err(job, "sandbox", err.Error()), nil
	}
	defer root.Close()
	f, err := root.Create(rel)
	if err != nil {
		return params.Err(job, "sandbox", err.Error()), nil
	}
	if _, err := f.Write(body); err != nil {
		f.Close()
		return params.Err(job, "sandbox", err.Error()), nil
	}
	if err := f.Close(); err != nil {
		return params.Err(job, "sandbox", err.Error()), nil
	}

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"pdf": {MIME: "application/pdf", Ref: dest},
			"meta": {MIME: "application/json", Inline: map[string]any{
				"spreadsheet_id": id, "path": dest, "bytes": len(body), "mime": "application/pdf",
			}},
		},
	}, nil
}
