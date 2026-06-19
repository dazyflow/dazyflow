package transform

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "render_template",
			Version:     "1.0",
			Label:       "Render template",
			Subtitle:    "Fill an HTML template",
			Color:       "#a78bfa",
			Icon:        "code",
			Category:    "transformation",
			Provider:    "internal",
			Tags:        []string{"transform", "template", "html", "email", "render", "merge", "format"},
			Summary:     "Fill an HTML template with merge fields, producing a styled HTML string for email_send / gmail_send_email.",
			Description: "Render an HTML template with merge fields into one HTML string — your own branded layout, filled with dynamic content, ready to wire into an email's Body. The template is Go html/template syntax: {{.name}} pulls a field from the data, {{range .items}}…{{end}} loops, {{if .vip}}…{{end}} branches. Type the template on the step, or wire it in from a workspace file (file_read of a .html). Wire the merge data (a JSON object, e.g. a row from a sheet or a webhook body) into the 'data' input — the template sees it as the root, so {{.customer}} reads data.customer. Values are HTML-escaped automatically, so a customer name containing <script> can't break your markup. The 'html' output drops straight into email_send.body or gmail_send_email.body (set that step's format to HTML).",
			Examples: []core.ParamsExample{
				{
					Title:  "Branded greeting",
					Params: json.RawMessage(`{"template":"<h1>Hi {{.name}},</h1><p>Your order <b>{{.order_id}}</b> shipped.</p>"}`),
					Notes:  "Wire the data input (e.g. a sheet row with name + order_id) and send the 'html' output into email_send.body with format=HTML.",
				},
				{
					Title:  "Loop over line items",
					Params: json.RawMessage(`{"template":"<ul>{{range .items}}<li>{{.qty}}× {{.name}}</li>{{end}}</ul>"}`),
					Notes:  "Pass data like {\"items\":[{\"qty\":2,\"name\":\"Widget\"}]} into the 'data' input.",
				},
				{
					Title:  "Default for a missing field",
					Params: json.RawMessage(`{"template":"<p>Hello {{.name | default \"there\"}}!</p>"}`),
					Notes:  "Helpers: default, upper, lower, join. default fills in when the field is empty or absent.",
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				// template: typed on the step, or wired from a file (file_read
				// of a .html), in which case the input overrides the param.
				{Port: "template", Label: "Template", Required: false, MIME: []string{"text/html", "text/plain"}},
				// data: the merge context the template sees as the root (.).
				// A JSON object is the common shape (a sheet row, a webhook
				// body); an array works too for a top-level {{range .}}.
				{Port: "data", Label: "Data", Required: false, MIME: []string{"application/json"}},
			},
			Outputs: []core.Port{
				// Declared as both text/html (what it is) and text/plain so the
				// wire into email_send.body / gmail_send_email.body (text/plain
				// ports) is MIME-compatible — those ports accept any string and
				// the step's own format=HTML flag decides how it's sent.
				{Port: "html", Label: "Rendered HTML", MIME: []string{"text/html", "text/plain"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"template": {
						"type":"string",
						"format":"multiline",
						"title":"Template",
						"description":"HTML template (Go html/template). {{.field}} inserts a value from the data, {{range .items}}…{{end}} loops, {{if .x}}…{{end}} branches. Helpers: default, upper, lower, join. Values are auto-escaped. Overridden by the 'Template' input."
					},
					"data": {
						"type":"object",
						"title":"Data",
						"description":"Merge data the template sees as the root context, so {{.name}} reads data.name. Usually wired in via the 'Data' input (which overrides this); set here for fixed or test content."
					}
				}
			}`),
			Idempotent: true,
		},
		Execute: executeRenderTemplate,
	})
}

// maxRenderBytes caps the rendered output. html/template has no built-in
// size or time budget, so a template that ranges over a large data input
// (or expands explosively) could otherwise allocate unbounded memory and
// ignore cancellation. The cap is far above any real email body but turns
// a runaway render into a clean error instead of an OOM. Generous because
// emails with inlined CSS + a long table are legitimately large.
const maxRenderBytes = 8 << 20 // 8 MiB

// templateFuncs is the small, deliberately-safe helper set exposed to
// templates. All are pure string/JSON ops — nothing that touches the
// filesystem, network, or process — so an authored template can't reach
// outside the render.
var templateFuncs = template.FuncMap{
	// default returns fallback when v is nil or an empty string; otherwise v.
	// Usage: {{.name | default "there"}}
	"default": func(fallback, v any) any {
		if v == nil {
			return fallback
		}
		if s, ok := v.(string); ok && s == "" {
			return fallback
		}
		return v
	},
	"upper": strings.ToUpper,
	"lower": strings.ToLower,
	// join concatenates a list with sep. Accepts []string or []any of
	// stringables so it works on JSON-decoded arrays.
	"join": func(sep string, list any) string {
		switch xs := list.(type) {
		case []string:
			return strings.Join(xs, sep)
		case []any:
			parts := make([]string, len(xs))
			for i, x := range xs {
				parts[i] = fmt.Sprintf("%v", x)
			}
			return strings.Join(parts, sep)
		default:
			return fmt.Sprintf("%v", list)
		}
	},
}

// executeRenderTemplate renders an HTML template with merge data into a
// single HTML string on the `html` output. The template comes from the
// `template` input (a wired .html file) or the `template` param; the
// merge context comes from the `data` input or the `data` param. The
// input wins over the param in each case, matching the message sinks'
// "input overrides param" convention. Auto-escaping (html/template) is
// the injection defense: data values are escaped for their HTML context,
// so untrusted content (a customer name, a comment) can't inject markup.
func executeRenderTemplate(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	tmplText, ok := templateTextInputOr(job, paramStringOr(job.Params, "template", ""))
	if !ok {
		return errResult(job, "bad_input", "the 'Template' input must be text"), nil
	}
	if strings.TrimSpace(tmplText) == "" {
		return errResult(job, "bad_param", "render_template needs a 'template' — type one or wire the 'Template' input"), nil
	}

	data, err := resolveTemplateData(job)
	if err != nil {
		return errResult(job, "bad_input", err.Error()), nil
	}

	tmpl, err := template.New("render").Funcs(templateFuncs).Parse(tmplText)
	if err != nil {
		// A parse error is the author's mistake (mismatched {{ }}, bad
		// action) — surface it as a param error with the engine's message.
		return errResult(job, "bad_param", fmt.Sprintf("template: %v", err)), nil
	}

	var buf strings.Builder
	lw := &limitedWriter{w: &buf, limit: maxRenderBytes}
	if err := tmpl.Execute(lw, data); err != nil {
		if lw.tripped {
			return errResult(job, "too_large", fmt.Sprintf("rendered output exceeds the %d-byte limit", maxRenderBytes)), nil
		}
		// An execution error (e.g. calling a missing method, bad range
		// operand) — report which expression failed via the wrapped message.
		return errResult(job, "eval", fmt.Sprintf("render: %v", err)), nil
	}

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"html": {MIME: "text/html", Inline: buf.String()},
		},
	}, nil
}

// templateTextInputOr returns the text wired into the `template` input
// (string or raw bytes), or fallback when that port is unwired/empty. ok
// is false only when the port carries a NON-text value — a wiring mistake
// the caller rejects. Mirrors emailTextInputOr in the notify drop.
func templateTextInputOr(job core.Job, fallback string) (string, bool) {
	in, present := job.Input["template"]
	if !present || in.Inline == nil {
		return fallback, true
	}
	switch v := in.Inline.(type) {
	case string:
		if v != "" {
			return v, true
		}
		return fallback, true
	case []byte:
		if len(v) > 0 {
			return string(v), true
		}
		return fallback, true
	}
	return "", false
}

// resolveTemplateData builds the root context the template renders
// against: the `data` input when wired, else the `data` param, else an
// empty object. A JSON string is parsed; an object or array passes
// through. Anything else is an error rather than a silent surprise.
func resolveTemplateData(job core.Job) (any, error) {
	if in, present := job.Input["data"]; present && in.Inline != nil {
		return normalizeTemplateData(in.Inline)
	}
	if raw, ok := job.Params["data"]; ok && raw != nil {
		return normalizeTemplateData(raw)
	}
	// No data: render against an empty object so {{.x}} yields "" rather
	// than failing — a template with no merge fields still works.
	return map[string]any{}, nil
}

func normalizeTemplateData(inline any) (any, error) {
	switch v := inline.(type) {
	case map[string]any, []any, []map[string]any, map[string]string:
		return v, nil
	case string:
		if strings.TrimSpace(v) == "" {
			return map[string]any{}, nil
		}
		var parsed any
		if err := json.Unmarshal([]byte(v), &parsed); err != nil {
			return nil, fmt.Errorf("data JSON: %w", err)
		}
		return parsed, nil
	case []byte:
		if len(v) == 0 {
			return map[string]any{}, nil
		}
		var parsed any
		if err := json.Unmarshal(v, &parsed); err != nil {
			return nil, fmt.Errorf("data JSON: %w", err)
		}
		return parsed, nil
	case nil:
		return map[string]any{}, nil
	}
	return nil, fmt.Errorf("data: unsupported input type %T", inline)
}

// limitedWriter caps how many bytes a render may produce. Past the limit
// Write returns an error (tripping html/template's Execute) and records
// tripped so the caller can distinguish a size overflow from an ordinary
// template error.
type limitedWriter struct {
	w       *strings.Builder
	limit   int
	written int
	tripped bool
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.written+len(p) > l.limit {
		l.tripped = true
		return 0, fmt.Errorf("output limit exceeded")
	}
	n, err := l.w.Write(p)
	l.written += n
	return n, err
}
