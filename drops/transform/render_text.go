// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package transform

import (
	"context"
	"encoding/json"
	"errors"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/engine"
	"git.sr.ht/~klahr/dazyflow/internal/rendertext"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "render_text",
			Version:     "1.0",
			Label:       "Render text",
			Subtitle:    "Make text from items",
			Icon:        "text",
			Category:    "transformation",
			Provider:    "internal",
			Tags:        []string{"transform", "text", "render", "format", "join", "reduce", "message", "notify"},
			Description: "Collapse a list of rows into one text string: render one line per row (a CEL expression or a single column), then join the lines with a separator. This is the bridge between the tabular drops and the message sinks — `slack_send_message`, `gmail_send_email`, `github_create_issue` all want a single string on their `body`/`text` input, not a rows list. Wire `render_text.text` into that port. With zero rows it emits `empty` (default \"\"), so you can post \"No new orders today.\" instead of failing on an empty message.",
			Summary:     "Render rows into one text string — a templated line per row, joined with a separator.",
			Examples: []core.ParamsExample{
				{
					Title:  "One Slack line per row",
					Params: json.RawMessage(`{"template":"'• ' + row.country + ': ' + string(row.orders) + ' orders'"}`),
					Notes:  "Each row becomes a bullet; lines are joined with a newline. Wire the 'text' output into slack_send_message's 'body' input.",
				},
				{
					Title:  "Join an existing column with commas",
					Params: json.RawMessage(`{"column":"email","separator":", "}`),
				},
				{
					Title:  "Header + bullets, with a fallback when empty",
					Params: json.RawMessage(`{"prefix":"*Daily summary*\n","template":"'• ' + row.line","empty":"Nothing to report today."}`),
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "rows", Label: "Rows", Required: true, MIME: []string{"application/json"}},
			},
			Outputs: []core.Port{
				{Port: "text", Label: "Rendered text", MIME: []string{"text/plain"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"template": {
						"type":"string",
						"x_advanced":true,
						"description":"CEL expression rendering one line per row. Sees the row as 'row' and the current time as 'now'. Non-string results are stringified. Takes precedence over 'column'."
					},
					"column": {
						"type":"string",
						"x_advanced":true,
						"description":"Name of a single column to take each line from. Used when 'template' is absent."
					},
					"separator": {
						"type":"string",
						"default":"\n",
						"x_advanced":true,
						"description":"String placed between rendered lines. Defaults to a newline."
					},
					"prefix": {"type":"string","x_advanced":true,"description":"Prepended to the joined text when at least one row is rendered."},
					"suffix": {"type":"string","x_advanced":true,"description":"Appended to the joined text when at least one row is rendered."},
					"empty": {"type":"string","x_advanced":true,"description":"Text emitted when there are zero input rows. Defaults to the empty string."}
				}
			}`),
			Idempotent: true,
		},
		Execute: executeRenderText,
	})
}

// executeRenderText reduces a rows list to a single string on the `text`
// output port. It is the deliberate inverse of the tabular drops: where
// compute_rows/map_rows take rows and emit rows, render_text takes rows
// and emits one scalar string — the shape the message sinks
// (slack_send_message.body, gmail_send_email.body, github_create_issue.body)
// actually consume. Without it, the only way to feed those ports was the
// ${upstream.node.rows[0].col} param trick, which can only reach row 0;
// render_text spans every row.
//
// Each row renders to a line via either a CEL `template` expression
// (sees `row` and `now`, like compute_rows) or a single `column`.
// Lines are joined with `separator`, wrapped in `prefix`/`suffix`. A
// per-row eval error fails the whole job rather than emitting a partial
// message — a half-rendered notification is worse than a clear failure.
// With zero rows, none of prefix/suffix/separator apply; the output is
// the `empty` string verbatim, so an empty result set yields a chosen
// fallback ("Nothing to report.") instead of an empty message the sink
// would reject.
// executeRenderText reads the `rows` input, then defers the actual rendering
// (CEL compile, per-row eval, join, prefix/suffix, empty fallback) to the
// shared internal/rendertext package — the SAME code the editor's live-preview
// endpoint runs, so a previewed template renders byte-identically at run time.
// maxBytes is 0 (no ceiling) at run time; the preview imposes a small cap.
func executeRenderText(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	rowsRef, ok := job.Input["rows"]
	if !ok {
		return errResult(job, "missing_input", "input port 'rows' is required"), nil
	}
	rows, err := normalizeRows(rowsRef.Inline)
	if err != nil {
		return errResult(job, "bad_input", err.Error()), nil
	}

	text, err := rendertext.Render(ctx, rendertext.SpecFromParams(job.Params), rows, 0)
	if err != nil {
		var pe *rendertext.ParseError
		var ee *rendertext.EvalError
		switch {
		case errors.Is(err, rendertext.ErrNoRenderer), errors.As(err, &pe):
			// No renderer configured, or the CEL template doesn't compile —
			// both are author mistakes in the step's params.
			return errResult(job, "bad_param", err.Error()), nil
		case errors.As(err, &ee):
			return errResult(job, "eval", err.Error()), nil
		default:
			return errResult(job, "internal", err.Error()), nil
		}
	}
	return renderTextResult(job, text), nil
}

func renderTextResult(job core.Job, text string) core.Result {
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"text": {MIME: "text/plain", Inline: text},
		},
	}
}

// paramStringOr reads an optional string param, returning def when the
// key is absent or not a string.
func paramStringOr(params map[string]any, key, def string) string {
	if raw, ok := params[key]; ok {
		if s, ok := raw.(string); ok {
			return s
		}
	}
	return def
}
