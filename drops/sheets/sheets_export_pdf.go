// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package sheets

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
	"git.sr.ht/~klahr/dazyflow/drops/internal/sandbox"
	"git.sr.ht/~klahr/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "sheets_export_pdf",
			Version:     "1.0",
			Label:       "Google Sheets",
			Subtitle:    "Export PDF",
			Summary:     "Turn a Google Sheet into a PDF file.",
			Description: "Turn a Google Sheet into a PDF file. Connect the 'PDF' output into a step that takes files — e.g. Gmail's Attachments to email it. The file lives in the run's scratch space (advanced: override the file name via 'path').",
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
			Inputs: []core.Port{
				// Optional: wire a spreadsheet id in to override the picker, so a
				// reference can be threaded from an upstream sheet step.
				{Port: "spreadsheet_id", Label: "Spreadsheet ID", MIME: []string{"text/plain"}},
			},
			Outputs: []core.Port{
				{Port: "pdf", Label: "PDF", MIME: []string{"application/pdf"}},
				// The structured result is still EMITTED under "meta" for run
				// records/debugging but not declared as a pin (same as append
				// rows and gmail). spreadsheet_id is re-emitted so any sheet
				// step downstream can target the same spreadsheet by wire.
				{Port: "spreadsheet_id", Label: "Spreadsheet ID", MIME: []string{"text/plain"}},
				{Port: "meta", Label: "Details", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"account":{"type":"string","default":"default"},
					"spreadsheet_id":{"type":"string","format":"google-spreadsheet","title":"Spreadsheet","description":"The spreadsheet to export."},
					"path":{"type":"string","title":"File name","examples":["Survey results.pdf"],"description":"What to call the PDF — also the attachment name when emailed. '.pdf' is added for you; leave blank for an automatic name."},
					"timeout_ms":{"type":"integer","default":30000,"minimum":1,"description":"Hard deadline for the request, in milliseconds."}
				},
				"required":["spreadsheet_id"]
			}`),
			Idempotent: true,
		},
		Execute: executeSheetsExportPDF,
	})
}

func executeSheetsExportPDF(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	id := resolveSpreadsheetID(job)
	if id == "" {
		return params.Err(job, "bad_param", "'spreadsheet_id' is required"), nil
	}
	token, err := resolveToken(ctx, job)
	if err != nil {
		return params.Err(job, "auth", err.Error()), nil
	}
	// 'path' is a plain file name ("Svar.pdf") — non-techies shouldn't see
	// the scratch:// scheme. A bare name lands in the run's scratch space;
	// ".pdf" is appended when missing; an explicit scheme (scratch://… from
	// older flows or templates) passes through untouched.
	dest := strings.TrimSpace(params.StringDefault(job.Params, "path", ""))
	switch {
	case dest == "":
		dest = sandbox.Scheme + "sheet-" + id + ".pdf"
	case !strings.Contains(dest, "://"):
		dest = sandbox.Scheme + dest
	}
	if !strings.HasSuffix(strings.ToLower(dest), ".pdf") {
		dest += ".pdf"
	}

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
			"spreadsheet_id": {MIME: "text/plain", Inline: id},
		},
	}, nil
}
