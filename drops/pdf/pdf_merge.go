// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package pdf

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	pdfapi "github.com/pdfcpu/pdfcpu/pkg/api"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "pdf_merge",
			Version:     "1.0",
			Label:       "PDF",
			Subtitle:    "Combine PDFs",
			Summary:     "Join several PDFs into one file, in the order they're connected.",
			Description: "Join several PDFs into one. Connect file-producing steps into the Files input — the attachments a month of invoice emails carried, the pages a scanner sent one at a time — and they're combined in the order they arrive. The natural end of a filing flow: one file for the accountant instead of forty. Runs on this machine; nothing is uploaded anywhere.",
			Integration: integration,
			Category:    "io",
			Icon:        "combine",
			Color:       brandColor,
			Provider:    "internal",
			Tags:        []string{"pdf", "merge", "combine", "join", "documents", "invoice"},
			Examples: []core.ParamsExample{
				{
					Title:  "One file for the month",
					Params: json.RawMessage(`{"name":"invoices-june.pdf","save_into":"bookkeeping"}`),
					Notes:  "Connect Download attachments' First file into Files inside a For each — each pass adds a document.",
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				// Variadic: the whole point is several files, and requiring a
				// step per document would defeat it. Untyped MIME — a PDF ref
				// carries its own type per run.
				{Port: "files", Label: "Files", Required: true, Variadic: true},
			},
			Outputs: []core.Port{
				{Port: "pdf", Label: "PDF", MIME: []string{"application/pdf"}},
				{Port: "pages", Label: "Pages", MIME: []string{"text/plain"}, Example: json.RawMessage(`"18"`)},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"name":{"type":"string","title":"File name","examples":["invoices-june.pdf"],"description":"What to call the combined file. \".pdf\" is added if you leave it off."},
					"save_into":{"type":"string","title":"Save into","format":"workspace-path","description":"Workspace folder to save it in, so it outlives the run. Leave blank to keep it in the run's scratch area — fine when a later step emails or files it."},
					"divider":{"type":"boolean","title":"Blank page between documents","default":false,"description":"Insert a blank page between each document, so it's obvious where one invoice ends and the next begins on paper."}
				}
			}`),
			// Combining the same inputs produces the same file, and the write
			// goes to a path the author named — so a retry overwrites rather
			// than accumulating.
			Idempotent: true,
		},
		Execute: executePDFMerge,
	})
}

func executePDFMerge(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	refs := variadicRefs(job, "files")
	if len(refs) == 0 {
		return params.Err(job, "bad_input", "nothing to combine — connect at least one PDF into the Files input"), nil
	}

	readers := make([]io.ReadSeeker, 0, len(refs))
	for i, ref := range refs {
		if ctx.Err() != nil {
			return params.Err(job, "cancelled", ctx.Err().Error()), nil
		}
		data, bad := readPDF(job, ref, refName(ref, i))
		if bad != nil {
			return *bad, nil
		}
		readers = append(readers, bytes.NewReader(data))
	}

	// A single input is a legitimate degenerate case — a For each that
	// happened to find one invoice — and must not be an error. pdfcpu handles
	// it, so this is only worth a note rather than a branch.
	var out bytes.Buffer
	divider := params.BoolDefault(job.Params, "divider", false)
	if err := pdfapi.MergeRaw(readers, &out, divider, conf()); err != nil {
		return params.Err(job, "pdf_error", fmt.Sprintf("couldn't combine the files: %v", err)), nil
	}

	dest, bad := writeOut(job, strings.TrimSpace(params.StringDefault(job.Params, "save_into", "")),
		outputName(job, "name", "combined.pdf"), bytes.NewReader(out.Bytes()))
	if bad != nil {
		return *bad, nil
	}

	// The page count comes from reading the result back rather than adding up
	// the inputs: with a divider page inserted the arithmetic differs, and a
	// count that disagrees with the file is worse than no count.
	pages := 0
	if info, err := pdfapi.PDFInfo(bytes.NewReader(out.Bytes()), dest, nil, false, conf()); err == nil && info != nil {
		pages = info.PageCount
	}

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"pdf":   {MIME: "application/pdf", Ref: dest},
			"pages": {MIME: "text/plain", Inline: fmt.Sprint(pages)},
		},
	}, nil
}
