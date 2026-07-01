// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package transform

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "render_table",
			Version:     "1.0",
			Label:       "Make a table",
			Subtitle:    "HTML table from rows",
			Icon:        "table",
			Category:    "transformation",
			Provider:    "internal",
			Tags:        []string{"transform", "table", "html", "render", "format", "email", "message", "report"},
			Description: "Turn a rows list straight into a ready-to-send HTML table — the column names become the header row and every row becomes a table row. Unlike render_text there is no template to write and no column names to type: the headers come from whatever columns the data actually has, so it can't drift from the source (no \"no such key\" at run time). Wire a rows list into `rows` and the `html` output into a message sink — e.g. gmail_send_email's body. With zero rows it emits `empty` (default \"\") so an empty result yields a chosen fallback instead of a blank table.",
			Summary:     "Turn rows into a ready-to-send HTML table — columns become the header row, no template needed.",
			Examples: []core.ParamsExample{
				{
					Title:  "Email a table of every column",
					Params: json.RawMessage(`{}`),
					Notes:  "Zero config: wire a rows list into 'rows' and the 'html' output into gmail_send_email's body. Headers are the data's columns, in order.",
				},
				{
					Title:  "Only some columns, in a chosen order",
					Params: json.RawMessage(`{"columns":["name","email","status"]}`),
				},
				{
					Title:  "Fallback when there are no rows",
					Params: json.RawMessage(`{"empty":"No orders today."}`),
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "rows", Label: "Rows", Required: true, MIME: []string{"application/json"}},
			},
			Outputs: []core.Port{
				// Declare text/plain too (like render_template) so the output is
				// MIME-compatible with text/plain body ports (gmail_send_email.body,
				// slack/discord message inputs) — they accept any string. The Ref
				// emitted at run time is text/html so HTML-aware sinks render it.
				{Port: "html", Label: "HTML table", MIME: []string{"text/html", "text/plain"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"columns": {"type":"array","items":{"type":"string"},"x_advanced":true,"description":"Which columns to include, in order. Leave empty to use every column the data has, in its natural order. The inspector's drag-to-reorder editor writes this for you."},
					"empty":   {"type":"string","title":"If there are no rows","description":"What to show instead of an empty table when the input has zero rows — plain text or HTML, e.g. \"No results yet\". Leave blank to output nothing at all."}
				}
			}`),
			Idempotent: true,
		},
		Execute: executeRenderTable,
	})
}

// executeRenderTable renders a rows list into one HTML <table> string on the
// `html` output port — the zero-config counterpart to render_text. Where
// render_text needs a CEL template that names columns (and so can crash with
// "no such key" when the data's casing differs), render_table reads the column
// set from the rows themselves at run time: headers are the rowset's column
// order, cells are looked up per row. There is nothing to keep in sync with the
// data, so a column-name mismatch is impossible by construction.
//
// Cell values and headers are HTML-escaped, so tenant data can't inject markup
// into the rendered table. With zero rows the output is the `empty` string
// verbatim (no empty <table>), mirroring render_text's empty-fallback.
func executeRenderTable(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	rowsOut, headers, errRes, ok := loadRowsAndHeaders(job)
	if !ok {
		return errRes, nil
	}

	// An explicit `columns` list overrides both the set and the order; absent,
	// we use every column the data carries (loadRowsAndHeaders' header order).
	if raw, present := job.Params["columns"]; present {
		cols, err := normalizeStringSlice(raw, "columns")
		if err != nil {
			return errResult(job, "bad_param", err.Error()), nil
		}
		if len(cols) > 0 {
			headers = cols
		}
	}

	if len(rowsOut) == 0 {
		return renderTableResult(job, paramStringOr(job.Params, "empty", "")), nil
	}
	return renderTableResult(job, buildHTMLTable(headers, rowsOut)), nil
}

const (
	tableStyle = "border-collapse:collapse;font-family:sans-serif;font-size:14px"
	thStyle    = "border:1px solid #ddd;padding:6px 10px;text-align:left;background:#f3f4f6"
	tdStyle    = "border:1px solid #ddd;padding:6px 10px"
)

// buildHTMLTable writes an inline-styled table (inline CSS so it survives email
// clients, which strip <style>). Headers are the raw column names — "first row
// is the column names" — not humanized, so what the user sees matches the data.
func buildHTMLTable(headers []string, rows []map[string]any) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<table style=%q><thead><tr>`, tableStyle)
	for _, h := range headers {
		fmt.Fprintf(&b, `<th style=%q>%s</th>`, thStyle, html.EscapeString(h))
	}
	b.WriteString("</tr></thead><tbody>")
	for _, row := range rows {
		b.WriteString("<tr>")
		for _, h := range headers {
			fmt.Fprintf(&b, `<td style=%q>%s</td>`, tdStyle, html.EscapeString(cellString(row[h])))
		}
		b.WriteString("</tr>")
	}
	b.WriteString("</tbody></table>")
	return b.String()
}

// cellString renders a cell value as plain text (escaped by the caller). A
// missing/nil cell is blank rather than "<nil>".
func cellString(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

func renderTableResult(job core.Job, htmlStr string) core.Result {
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"html": {MIME: "text/html", Inline: htmlStr},
		},
	}
}
