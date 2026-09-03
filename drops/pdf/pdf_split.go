// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package pdf

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	pdfapi "github.com/pdfcpu/pdfcpu/pkg/api"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/engine"
)

// maxParts caps how many files one split may produce, so a 2000-page scan
// split per page can't fill the run's scratch area with 2000 documents.
const maxParts = 200

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "pdf_split",
			Version:     "1.0",
			Label:       "PDF",
			Subtitle:    "Split a PDF",
			Summary:     "Cut one PDF into several — a file per page, or every N pages.",
			Description: "Cut one PDF into several files. Set 'Pages per file' to 1 and a batch scan becomes one document per page, ready to loop over with For each and file or read individually. Set it to 2 for double-sided scans, or higher for fixed-length statements. Each piece comes out as a file the next step can open. Runs on this machine; nothing is uploaded anywhere.",
			Integration: integration,
			Category:    "io",
			Icon:        "scissors",
			Color:       brandColor,
			Provider:    "internal",
			Tags:        []string{"pdf", "split", "pages", "scan", "batch"},
			Examples: []core.ParamsExample{
				{
					Title:  "One file per page from a batch scan",
					Params: json.RawMessage(`{"pages_per_file":1}`),
					Notes:  "Connect Files into a For each to handle each page — read it with an AI step, or file it.",
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "file", Label: "File", Required: true},
			},
			Outputs: []core.Port{
				{Port: "files", Label: "Files", MIME: []string{"application/json"}},
				{Port: "first", Label: "First file"},
				{Port: "count", Label: "How many", MIME: []string{"text/plain"}, Example: json.RawMessage(`"4"`)},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"pages_per_file":{"type":"integer","title":"Pages per file","default":1,"minimum":1,"maximum":500,"description":"How many pages each piece gets. 1 makes a file per page."},
					"save_into":{"type":"string","title":"Save into","format":"workspace-path","description":"Workspace folder to save the pieces in, so they outlive the run. Leave blank to keep them in the run's scratch area."},
					"name":{"type":"string","title":"Name prefix","examples":["invoice"],"description":"What to call the pieces — they get \"-1\", \"-2\" and so on. Leave blank to use \"page\"."}
				}
			}`),
			Idempotent: true,
		},
		Execute: executePDFSplit,
	})
}

func executePDFSplit(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	ref, ok := job.Input["file"]
	if !ok || (ref.Ref == "" && ref.Inline == nil) {
		return params.Err(job, "bad_input", "no file — connect a PDF into the File input"), nil
	}
	data, bad := readPDF(job, ref, refName(ref, 0))
	if bad != nil {
		return *bad, nil
	}
	span := params.ClampInt(params.IntDefault(job.Params, "pages_per_file", 1), 1, 500)
	if ctx.Err() != nil {
		return params.Err(job, "cancelled", ctx.Err().Error()), nil
	}

	spans, err := pdfapi.SplitRaw(bytes.NewReader(data), span, conf())
	if err != nil {
		if isEncryptedErr(err) {
			return params.Err(job, "encrypted", fmt.Sprintf("%s is password-protected, so it can't be split", refName(ref, 0))), nil
		}
		return params.Err(job, "pdf_error", fmt.Sprintf("couldn't split %s: %v", refName(ref, 0), err)), nil
	}
	if len(spans) > maxParts {
		return params.Err(job, "too_large", fmt.Sprintf("that would make %d files, over the %d-file limit — use a larger 'Pages per file'", len(spans), maxParts)), nil
	}

	saveInto := strings.TrimSpace(params.StringDefault(job.Params, "save_into", ""))
	prefix := strings.TrimSpace(params.StringDefault(job.Params, "name", ""))
	if prefix == "" {
		prefix = "page"
	}
	prefix = strings.TrimSuffix(prefix, ".pdf")

	rows := make([]map[string]any, 0, len(spans))
	var first core.Ref
	for i, s := range spans {
		if ctx.Err() != nil {
			return params.Err(job, "cancelled", ctx.Err().Error()), nil
		}
		if s == nil || s.Reader == nil {
			continue
		}
		dest, bad := writeOut(job, saveInto, fmt.Sprintf("%s-%d.pdf", prefix, i+1), s.Reader)
		if bad != nil {
			return *bad, nil
		}
		rows = append(rows, map[string]any{
			"path": dest,
			"name": fmt.Sprintf("%s-%d.pdf", prefix, i+1),
			// The page range each piece covers, so a flow can label or route
			// by it rather than counting along in a loop.
			"from": s.From,
			"to":   s.Thru,
		})
		if i == 0 {
			first = core.Ref{MIME: "application/pdf", Ref: dest}
		}
	}
	if len(rows) == 0 {
		return params.Err(job, "pdf_error", fmt.Sprintf("%s produced no pages — it may be empty or damaged", refName(ref, 0))), nil
	}

	out := map[string]core.Ref{
		"files": {MIME: "application/json", Inline: rows, Headers: []string{"name", "path", "from", "to"}},
		"count": {MIME: "text/plain", Inline: fmt.Sprint(len(rows))},
		"first": first,
	}
	return core.Result{JobID: job.ID, Status: core.StatusOK, Output: out}, nil
}
