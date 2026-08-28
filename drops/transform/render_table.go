// SPDX-FileCopyrightText: 2026 Angels' Ware
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
			Description: "Turn a rows list straight into a ready-to-send HTML table — the column names become the header row and every row becomes a table row. Unlike Make text there is no template to write and no column names to type: the headers come from whatever columns the data actually has, so it can't drift from the source (no \"no such key\" at run time). Connect a rows list into `rows` and the `html` output into a message step — e.g. a Send email step's Body. Headers are the data's own column names unless you rename them. `column_labels` is a plain {column: heading} map — {\"customer_email\":\"Customer\"} heads that column \"Customer\" and leaves every other column alone, so renaming one heading doesn't mean listing them all. `columns` can also carry a per-entry `label`, which wins over the map for that column. Set `title` to name the table and it renders as a caption above the header row — a ${upstream.…} reference works there, so the name can carry the run's data (\"Orders for 2026-08-26\"). With zero rows it emits `empty` (default \"\") so an empty result yields a chosen fallback instead of a blank table.",
			Summary:     "Turn rows into a ready-to-send HTML table — columns become the header row, no template needed.",
			Examples: []core.ParamsExample{
				{
					Title:  "Email a table of every column",
					Params: json.RawMessage(`{}`),
					Notes:  "Zero config: connect a rows list into 'rows' and the 'html' output into a Send email step's Body. Headers are the data's columns, in order.",
				},
				{
					Title:  "A named table",
					Params: json.RawMessage(`{"title":"Open orders"}`),
					Notes:  "Renders as a caption above the header row. Use a reference — \"Orders for ${upstream.date.out}\" — to name it from the run's own data.",
				},
				{
					Title:  "Only some columns, in a chosen order",
					Params: json.RawMessage(`{"columns":["name","email","status"]}`),
				},
				{
					Title:  "Readable headers over technical column names",
					Params: json.RawMessage(`{"column_labels":{"customer_email":"Customer","created_at":"Ordered"}}`),
					Notes:  "Renames those two headings and leaves the rest of the columns as they are. The cells still come from the named columns.",
				},
				{
					Title:  "Chosen columns, in order, with their own headings",
					Params: json.RawMessage(`{"columns":[{"column":"customer_email","label":"Customer"},{"column":"created_at","label":"Ordered"}]}`),
					Notes:  "Use this when the selection, the order and the headings are all being set together; a per-entry `label` wins over `column_labels`.",
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
					"title":   {"type":"string","title":"Table name","description":"An optional name for the table, shown as a caption above the header row. Leave blank for no caption. Takes a reference, so it can name the run's own data — e.g. \"Orders for ${upstream.today.out}\"."},
					"column_labels": {"type":"object","additionalProperties":{"type":"string"},"title":"Column names","x_key_placeholder":"customer_email","x_value_placeholder":"Customer","description":"Rename headers: the column as it appears in the data on the left, the heading you want on the right. Columns you don't list keep their own name, and listing one does not hide the others — this only changes the text in the header row."},
					"columns": {"type":"array","items":{"oneOf":[{"type":"string"},{"type":"object","properties":{"column":{"type":"string"},"label":{"type":"string"}},"required":["column"]}]},"x_advanced":true,"description":"Which columns to include, in order. A plain name uses the data's own column name as the header; {\"column\":\"customer_email\",\"label\":\"Customer\"} keeps reading that column but heads it \"Customer\". Leave empty to use every column the data has, in its natural order. The inspector's column editor writes this for you."},
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

	// An explicit `columns` list overrides the set, the order AND the header
	// text; absent, we use every column the data carries (loadRowsAndHeaders'
	// header order), each headed by its own name.
	cols := dataColumns(headers)
	if raw, present := job.Params["columns"]; present {
		chosen, err := parseTableColumns(raw)
		if err != nil {
			return errResult(job, "bad_param", err.Error()), nil
		}
		if len(chosen) > 0 {
			cols = chosen
		}
	}

	// Renames from the {column: heading} map, applied to every column that
	// didn't state a heading of its own in `columns`. Most specific wins, the
	// same shape as Sort rows' Direction against its per-column prefixes.
	if raw, present := job.Params["column_labels"]; present && raw != nil {
		labels, err := normalizeStringMap(raw, "column_labels")
		if err != nil {
			return errResult(job, "bad_param", err.Error()), nil
		}
		for i := range cols {
			if cols[i].named {
				continue
			}
			if l, ok := labels[cols[i].key]; ok && strings.TrimSpace(l) != "" {
				cols[i].label = l
			}
		}
	}

	// Zero rows emits the `empty` fallback verbatim — no table, and so no
	// caption either. A caption with nothing under it would be a heading for a
	// table that isn't there, and `empty` is the whole message in that case.
	if len(rowsOut) == 0 {
		return renderTableResult(job, paramStringOr(job.Params, "empty", "")), nil
	}
	title := strings.TrimSpace(paramStringOr(job.Params, "title", ""))
	return renderTableResult(job, buildHTMLTable(title, cols, rowsOut)), nil
}

// tableColumn is one column of the output: `key` is the row field the cells
// come from, `label` is what the header says. They are the same thing for a
// plainly-named column, and deliberately separable for a table meant to be
// read by a person — `customer_email` is the data's name for it, "Customer" is
// the reader's.
//
// The split is what makes renaming a header work at all. The inspector's
// column editor has always offered "tap a column to rename it", and with only
// a name to write it wrote the new name into `columns` as the KEY: the header
// read "Customer" and every cell under it came out blank, because no row has a
// field called "Customer". The header text and the field name are two
// different facts and the param now carries both.
type tableColumn struct {
	key   string
	label string
	// named records that this column's heading was set on the column itself
	// (a `label` in its `columns` entry), so the column_labels map leaves it
	// alone. Without it the map would quietly overrule the more specific
	// setting, which is the wrong way round.
	named bool
}

// dataColumns heads each of the data's own columns with its own name — the
// zero-config default.
func dataColumns(headers []string) []tableColumn {
	out := make([]tableColumn, len(headers))
	for i, h := range headers {
		out[i] = tableColumn{key: h, label: h}
	}
	return out
}

// parseTableColumns reads the `columns` param. An entry is either a bare name
// (header = the data's name for it) or {"column":…,"label":…} (header = label,
// cells still from `column`). A blank or missing label falls back to the key,
// so {"column":"name"} is exactly the same as "name".
//
// Both shapes coexist because most columns want neither renaming nor the
// clutter of an object, and the editor writes whichever shape a column
// actually needs.
func parseTableColumns(v any) ([]tableColumn, error) {
	switch list := v.(type) {
	case []string:
		return dataColumns(list), nil
	case []tableColumn:
		// Native callers (tests) may build the columns directly.
		return list, nil
	case []any:
		out := make([]tableColumn, 0, len(list))
		for i, item := range list {
			switch it := item.(type) {
			case string:
				if it == "" {
					continue
				}
				out = append(out, tableColumn{key: it, label: it})
			case map[string]any:
				key, _ := it["column"].(string)
				if key == "" {
					return nil, fmt.Errorf("columns[%d]: 'column' missing", i)
				}
				label, _ := it["label"].(string)
				named := strings.TrimSpace(label) != ""
				if !named {
					label = key
				}
				out = append(out, tableColumn{key: key, label: label, named: named})
			default:
				return nil, fmt.Errorf("columns[%d]: expected a name or {column,label}, got %T", i, item)
			}
		}
		return out, nil
	}
	return nil, fmt.Errorf("columns: expected array of names or {column,label} objects, got %T", v)
}

const (
	tableStyle = "border-collapse:collapse;font-family:sans-serif;font-size:14px"
	// A caption is a heading, so it reads as one: a size up from the cells,
	// semibold, and left-aligned (a caption centres by default, which floats
	// oddly over a left-aligned table).
	captionStyle = "font-family:sans-serif;font-size:15px;font-weight:600;text-align:left;padding:0 0 6px"
	thStyle      = "border:1px solid #ddd;padding:6px 10px;text-align:left;background:#f3f4f6"
	tdStyle      = "border:1px solid #ddd;padding:6px 10px"
)

// buildHTMLTable writes an inline-styled table (inline CSS so it survives email
// clients, which strip <style>). Headers are the raw column names — "first row
// is the column names" — not humanized, so what the user sees matches the data.
//
// A non-empty title becomes a <caption>, which is the element HTML has for
// naming a table: it travels INSIDE the <table>, so pasting the output into an
// email body or a message keeps the name attached to the rows it belongs to,
// where a separate <h3> would come apart the first time something re-wrapped
// the markup. Escaped like every other author- or tenant-supplied string here.
func buildHTMLTable(title string, cols []tableColumn, rows []map[string]any) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<table style=%q>`, tableStyle)
	// Must precede <thead>: a caption anywhere else in a table is invalid and
	// browsers relocate it.
	if title != "" {
		fmt.Fprintf(&b, `<caption style=%q>%s</caption>`, captionStyle, html.EscapeString(title))
	}
	b.WriteString(`<thead><tr>`)
	for _, c := range cols {
		fmt.Fprintf(&b, `<th style=%q>%s</th>`, thStyle, html.EscapeString(c.label))
	}
	b.WriteString("</tr></thead><tbody>")
	for _, row := range rows {
		b.WriteString("<tr>")
		for _, c := range cols {
			fmt.Fprintf(&b, `<td style=%q>%s</td>`, tdStyle, html.EscapeString(cellString(row[c.key])))
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
