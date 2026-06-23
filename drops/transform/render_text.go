package transform

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/cel-go/cel"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/engine"
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
						"description":"CEL expression rendering one line per row. Sees the row as 'row' and the current time as 'now'. Non-string results are stringified. Takes precedence over 'column'."
					},
					"column": {
						"type":"string",
						"description":"Name of a single column to take each line from. Used when 'template' is absent."
					},
					"separator": {
						"type":"string",
						"default":"\n",
						"description":"String placed between rendered lines. Defaults to a newline."
					},
					"prefix": {"type":"string","description":"Prepended to the joined text when at least one row is rendered."},
					"suffix": {"type":"string","description":"Appended to the joined text when at least one row is rendered."},
					"empty": {"type":"string","description":"Text emitted when there are zero input rows. Defaults to the empty string."}
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
func executeRenderText(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	rowsRef, ok := job.Input["rows"]
	if !ok {
		return errResult(job, "missing_input", "input port 'rows' is required"), nil
	}
	rows, err := normalizeRows(rowsRef.Inline)
	if err != nil {
		return errResult(job, "bad_input", err.Error()), nil
	}

	separator := paramStringOr(job.Params, "separator", "\n")
	prefix := paramStringOr(job.Params, "prefix", "")
	suffix := paramStringOr(job.Params, "suffix", "")
	empty := paramStringOr(job.Params, "empty", "")

	if len(rows) == 0 {
		return renderTextResult(job, empty), nil
	}

	template := paramStringOr(job.Params, "template", "")
	column := paramStringOr(job.Params, "column", "")
	if template == "" && column == "" {
		return errResult(job, "bad_param", "render_text needs either a 'template' expression or a 'column' name"), nil
	}

	var prog cel.Program
	if template != "" {
		env, err := newRowCELEnv()
		if err != nil {
			return errResult(job, "internal", fmt.Sprintf("cel env: %v", err)), nil
		}
		prog, err = compileRowExpr(env, template, "template")
		if err != nil {
			return errResult(job, "bad_param", err.Error()), nil
		}
	}

	lines := make([]string, 0, len(rows))
	for i, row := range rows {
		var line string
		if prog != nil {
			v, err := evalExpression(ctx, prog, row)
			if err != nil {
				return errResult(job, "eval", fmt.Sprintf("template row %d: %v", i, err)), nil
			}
			line = stringifyCell(v)
		} else {
			line = stringifyCell(row[column])
		}
		lines = append(lines, line)
	}

	return renderTextResult(job, prefix+strings.Join(lines, separator)+suffix), nil
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

// stringifyCell renders a single cell value as the text that lands in a
// line. Strings pass through unquoted; everything else uses its natural
// Go formatting, with composite values (a map/list cell) JSON-encoded so
// they don't surface as Go's `map[...]` debug form.
func stringifyCell(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool, int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64, float32, float64:
		return fmt.Sprintf("%v", t)
	default:
		if b, err := json.Marshal(v); err == nil {
			return string(b)
		}
		return fmt.Sprintf("%v", v)
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
