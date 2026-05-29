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
	"git.sr.ht/~klahr/hazy-flow/integrations/internal/sandbox"
)

// sheets_export_pdf is the "give me this spreadsheet as a PDF" drop.
// Sheets itself has no native export endpoint — Drive renders the
// PDF, and because Sheets + Drive share the same Google OAuth app
// the same access token works once drive.readonly is in the scope
// list (see cmd/hzd/main.go where the scope is requested). The
// rendered bytes are written to a sandbox file (scratch:// by
// default — ephemeral, run-scoped) and the file Ref is the drop's
// output, ready to drop straight into gmail_send_email's variadic
// attachments port.

const defaultExportPDFTimeoutMs = 60000

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:             "sheets_export_pdf",
			Version:        "1.0",
			Label:          "Sheets export PDF",
			Color:          "#0F9D58",
			Icon:           "file-output",
			BrandLogo:      "/brands/sheets.svg",
			Category:       "network",
			Provider:       "internal",
			Integration:    "Google Sheets",
			Tags:           []string{"sheets", "google", "pdf", "export", "report"},
			Description:    "Render a Google Sheet as a PDF and stash it in the run's sandbox so a downstream node (typically gmail_send_email's attachments port) can pick it up. Drive's files.export does the rendering; this drop just handles auth, the sandbox write, and the Ref shape.",
			Summary:        "Export a Google Sheet to a PDF in the workspace sandbox, ready to attach to an email.",
			Examples: []core.ParamsExample{
				{
					Title:  "Daily report into scratch for gmail_send_email",
					Params: json.RawMessage(`{"account":"default","spreadsheet_id":"1AbcDEFghIJklmNOPqrsTUVwxyZ_0123456789abcd"}`),
					Notes:  "Defaults to writing scratch://sheet-<id>.pdf; the run's scratch tree is reclaimed when the flow finishes. Wire the 'pdf' output into gmail_send_email's 'attachments' port.",
				},
				{
					Title:  "Persist the PDF to the workspace under a fixed name",
					Params: json.RawMessage(`{"account":"default","spreadsheet_id":"1AbcDEFghIJklmNOPqrsTUVwxyZ_0123456789abcd","path":"reports/yesterday.pdf"}`),
					Notes:  "A plain (non-scratch://) path lands in the persistent workspace sandbox so later runs can still find it.",
				},
			},
			RequiresConnections: []core.ConnectionRequirement{
				{Kind: "oauth", Name: "sheets", Note: "Google OAuth — drive.readonly scope (for Drive's files.export)."},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Outputs: []core.Port{
				{Port: "pdf", Label: "PDF file ref", MIME: []string{"application/pdf"}},
				{Port: "meta", Label: "Export metadata", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"account":        {"type":"string","default":"default"},
					"token":          {"type":"string","description":"Raw access token; overrides 'account'."},
					"spreadsheet_id": {"type":"string","description":"The Google Sheet's file ID (the long string in its URL)."},
					"path":           {"type":"string","format":"workspace-path","description":"Sandbox destination. Defaults to scratch://sheet-<id>.pdf; use a plain path for a persistent workspace file."},
					"timeout_ms":     {"type":"integer","default":60000,"minimum":1,"description":"Hard deadline for the export, in milliseconds."}
				},
				"required":["spreadsheet_id"]
			}`),
			Idempotent:  true,
			RetryPolicy: core.RetryExponentialBackoff,
		},
		Execute: executeSheetsExportPDF,
	})
}

func executeSheetsExportPDF(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	spreadsheetID, err := params.String(job.Params, "spreadsheet_id")
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	token, err := resolveToken(ctx, job)
	if err != nil {
		return params.Err(job, "auth", err.Error()), nil
	}

	dest := params.StringDefault(job.Params, "path", "scratch://sheet-"+spreadsheetID+".pdf")
	timeoutMs := params.IntDefault(job.Params, "timeout_ms", defaultExportPDFTimeoutMs)

	// Open the sandbox root that owns dest before we burn any quota on
	// the HTTP call — a bad path is the cheapest failure mode to catch.
	root, rel, err := sandbox.OpenRoot(job, dest)
	if err != nil {
		return params.Err(job, "no_sandbox", err.Error()), nil
	}
	defer root.Close()

	exportURL := fmt.Sprintf("%s/files/%s/export?mimeType=%s",
		currentDriveHTTPBase(),
		url.PathEscape(spreadsheetID),
		url.QueryEscape("application/pdf"),
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, exportURL, nil)
	if err != nil {
		return params.Err(job, "internal", err.Error()), nil
	}
	req.Header.Set("Authorization", "Bearer "+token)

	client := &http.Client{Timeout: time.Duration(timeoutMs) * time.Millisecond}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return params.Err(job, "cancelled", ctx.Err().Error()), ctx.Err()
		}
		return params.Err(job, "drive_request_failed", err.Error()), nil
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// 5 MiB cap on the error body — Drive returns small JSON
		// envelopes here but the occasional HTML 5xx surfaces too.
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 5<<20))
		// 403 with insufficientPermissions is the canonical "the
		// account exists but drive.readonly wasn't granted" — surface
		// it with the actionable next step.
		if resp.StatusCode == http.StatusForbidden {
			return params.Err(job, "drive_forbidden",
				fmt.Sprintf("Drive returned 403: %s. The Google account needs to reconnect with the drive.readonly scope.",
					extractSheetsError(errBody))), nil
		}
		return params.Err(job, "drive_error",
			fmt.Sprintf("Drive returned %d: %s", resp.StatusCode, extractSheetsError(errBody))), nil
	}

	// Stream the response straight to the sandbox file. PDF renders
	// can be tens of MB; an io.Copy through a *os.Root-confined file
	// keeps memory bounded and traversal safe.
	out, err := root.Create(rel)
	if err != nil {
		if sandbox.IsEscape(err) {
			return params.Err(job, "sandbox_escape",
				fmt.Sprintf("dest %q escapes its sandbox root", dest)), nil
		}
		return params.Err(job, "io", fmt.Sprintf("create %q: %v", dest, err)), nil
	}
	defer out.Close()
	written, err := io.Copy(out, resp.Body)
	if err != nil {
		return params.Err(job, "io", fmt.Sprintf("write %q: %v", dest, err)), nil
	}

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			// Preserve the dest path verbatim (including scratch://) so
			// a downstream sandbox reader resolves it the same way.
			"pdf": {MIME: "application/pdf", Ref: dest},
			"meta": {MIME: "application/json", Inline: map[string]any{
				"spreadsheet_id": spreadsheetID,
				"path":           dest,
				"bytes":          written,
			}},
		},
	}, nil
}
