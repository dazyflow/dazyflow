// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package pdf

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	pdfapi "github.com/pdfcpu/pdfcpu/pkg/api"
	pdftypes "github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "pdf_info",
			Version:     "1.0",
			Label:       "PDF",
			Subtitle:    "Read PDF details",
			Summary:     "How many pages a PDF has, plus its title, author and dates.",
			Description: "Read a PDF's details without opening it: how many pages, its title and author, when it was made, whether it's password-protected. Useful as a check before doing work — branch on the page count to send a one-pager straight through and a fifty-page contract to a human, or spot an encrypted file before a later step fails on it. Runs on this machine; nothing is uploaded anywhere.",
			Integration: integration,
			Category:    "io",
			Icon:        "file-search",
			Color:       brandColor,
			Provider:    "internal",
			Tags:        []string{"pdf", "info", "pages", "metadata", "check"},
			Examples: []core.ParamsExample{
				{
					Title:  "Branch on how long it is",
					Params: json.RawMessage(`{}`),
					Notes:  "Connect a PDF into File; wire Pages into a Compare step to route long documents differently.",
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "file", Label: "File", Required: true},
			},
			Outputs: []core.Port{
				{Port: "pages", Label: "Pages", MIME: []string{"text/plain"}, Example: json.RawMessage(`"12"`)},
				{Port: "encrypted", Label: "Password-protected", MIME: []string{"text/plain"}, Example: json.RawMessage(`"false"`)},
				{Port: "info", Label: "Details", MIME: []string{"application/json"}, Example: json.RawMessage(`{"pages":12,"version":"1.7","title":"Faktura 4471","author":"Fortnox","subject":"Faktura","creator":"Fortnox Invoicing","producer":"pdfTeX","created":"2026-02-12T09:12:04Z","modified":"2026-02-12T09:12:04Z","encrypted":false,"keywords":"faktura,4471","size_bytes":48213,"page_size":"A4","tagged":true}`)},
			},
			ParamsSchema: json.RawMessage(`{"type":"object","properties":{}}`),
			Idempotent:   true,
		},
		Execute: executePDFInfo,
	})
}

func executePDFInfo(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	ref, ok := job.Input["file"]
	if !ok || (ref.Ref == "" && ref.Inline == nil) {
		return params.Err(job, "bad_input", "no file — connect a PDF into the File input"), nil
	}
	data, bad := readPDF(job, ref, refName(ref, 0))
	if bad != nil {
		return *bad, nil
	}
	if ctx.Err() != nil {
		return params.Err(job, "cancelled", ctx.Err().Error()), nil
	}

	info, err := pdfapi.PDFInfo(bytes.NewReader(data), refName(ref, 0), nil, false, conf())
	if err != nil {
		// A password-protected file is the one failure worth naming, because
		// the fix is a different file rather than a different step.
		if isEncryptedErr(err) {
			return params.Err(job, "encrypted", fmt.Sprintf("%s is password-protected, so its details can't be read", refName(ref, 0))), nil
		}
		return params.Err(job, "pdf_error", fmt.Sprintf("couldn't read %s: %v", refName(ref, 0), err)), nil
	}

	details := map[string]any{
		"pages":         info.PageCount,
		"version":       info.Version,
		"title":         info.Title,
		"author":        info.Author,
		"subject":       info.Subject,
		"creator":       info.Creator,
		"producer":      info.Producer,
		"created":       info.CreationDate,
		"modified":      info.ModificationDate,
		"encrypted":     info.Encrypted,
		"keywords":      info.Keywords,
		"size_bytes":    len(data),
		"page_size":     pageSize(info.PageDimensions),
		"tagged":        info.Tagged,
		"linearized":    info.Linearized,
		"using_objects": info.UsingObjectStreams,
	}

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			// Pages as text, not a number: it feeds a Compare or a Branch,
			// which is the whole reason someone reaches for this step, and
			// those speak the text/rows contract.
			"pages":     {MIME: "text/plain", Inline: fmt.Sprint(info.PageCount)},
			"encrypted": {MIME: "text/plain", Inline: fmt.Sprint(info.Encrypted)},
			"info":      {MIME: "application/json", Inline: details},
		},
	}, nil
}

// isEncryptedErr reports whether pdfcpu refused because the file needs a
// password. Matched on the message because pdfcpu returns plain errors here
// rather than typed ones.
func isEncryptedErr(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "password") || strings.Contains(msg, "encrypt")
}

// pageSize renders the document's page dimensions as "595 x 842" — enough to
// tell A4 from Letter from a receipt, without exposing pdfcpu's own types. A
// document with pages of several sizes lists them all, because that is itself
// worth knowing about a scan.
//
// Reads PageDimensions (the map pdfcpu populates) rather than its Dimensions
// slice, which only its CLI fills in. Sorted, because a map's iteration order
// would make the output differ run to run for no reason.
func pageSize(dims map[pdftypes.Dim]bool) string {
	if len(dims) == 0 {
		return ""
	}
	out := make([]string, 0, len(dims))
	for d := range dims {
		out = append(out, fmt.Sprintf("%.0f x %.0f", d.Width, d.Height))
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}
