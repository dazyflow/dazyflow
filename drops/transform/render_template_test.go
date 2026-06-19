package transform

import (
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

func runRenderTemplate(t *testing.T, params map[string]any, input map[string]core.Ref) core.Result {
	t.Helper()
	res, err := executeRenderTemplate(t.Context(), core.Job{Params: params, Input: input}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	return res
}

func renderedHTML(t *testing.T, params map[string]any, input map[string]core.Ref) string {
	t.Helper()
	res := runRenderTemplate(t, params, input)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if mime := res.Output["html"].MIME; mime != "text/html" {
		t.Errorf("html MIME = %q, want text/html", mime)
	}
	s, ok := res.Output["html"].Inline.(string)
	if !ok {
		t.Fatalf("html output is %T, want string", res.Output["html"].Inline)
	}
	return s
}

func TestRenderTemplate_MergeFields(t *testing.T) {
	got := renderedHTML(t,
		map[string]any{"template": "<h1>Hi {{.name}},</h1><p>Order {{.order_id}} shipped.</p>"},
		map[string]core.Ref{"data": {Inline: map[string]any{"name": "Acme", "order_id": "A-1"}}},
	)
	want := "<h1>Hi Acme,</h1><p>Order A-1 shipped.</p>"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRenderTemplate_RangeItems(t *testing.T) {
	got := renderedHTML(t,
		map[string]any{"template": "<ul>{{range .items}}<li>{{.qty}}× {{.name}}</li>{{end}}</ul>"},
		map[string]core.Ref{"data": {Inline: map[string]any{
			"items": []any{
				map[string]any{"qty": 2, "name": "Widget"},
				map[string]any{"qty": 1, "name": "Gadget"},
			},
		}}},
	)
	want := "<ul><li>2× Widget</li><li>1× Gadget</li></ul>"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestRenderTemplate_AutoEscape is the security property: a data value
// containing HTML must be escaped, not rendered as markup.
func TestRenderTemplate_AutoEscape(t *testing.T) {
	got := renderedHTML(t,
		map[string]any{"template": "<p>Hello {{.name}}</p>"},
		map[string]core.Ref{"data": {Inline: map[string]any{"name": `<script>alert(1)</script>`}}},
	)
	if strings.Contains(got, "<script>") {
		t.Fatalf("script tag was NOT escaped: %q", got)
	}
	if !strings.Contains(got, "&lt;script&gt;") {
		t.Errorf("expected escaped script entity, got %q", got)
	}
}

func TestRenderTemplate_DataFromJSONString(t *testing.T) {
	got := renderedHTML(t,
		map[string]any{"template": "{{.greeting}} {{.who}}"},
		map[string]core.Ref{"data": {Inline: `{"greeting":"Hej","who":"världen"}`}},
	)
	if got != "Hej världen" {
		t.Errorf("got %q", got)
	}
}

func TestRenderTemplate_InputOverridesParam(t *testing.T) {
	got := renderedHTML(t,
		map[string]any{"template": "PARAM {{.x}}"},
		map[string]core.Ref{
			"template": {Inline: "INPUT {{.x}}"},
			"data":     {Inline: map[string]any{"x": "v"}},
		},
	)
	if got != "INPUT v" {
		t.Errorf("input should override param template, got %q", got)
	}
}

func TestRenderTemplate_Helpers(t *testing.T) {
	got := renderedHTML(t,
		map[string]any{"template": `{{.name | default "there" | upper}} / {{join ", " .tags}}`},
		map[string]core.Ref{"data": {Inline: map[string]any{
			"tags": []any{"a", "b", "c"},
		}}},
	)
	if got != "THERE / a, b, c" {
		t.Errorf("got %q", got)
	}
}

func TestRenderTemplate_NoData(t *testing.T) {
	// A template with no merge fields renders fine with no data wired.
	got := renderedHTML(t, map[string]any{"template": "<p>Static</p>"}, nil)
	if got != "<p>Static</p>" {
		t.Errorf("got %q", got)
	}
}

func TestRenderTemplate_MissingTemplate(t *testing.T) {
	res := runRenderTemplate(t, map[string]any{}, nil)
	if res.Status != core.StatusError || res.Error == nil || res.Error.Code != "bad_param" {
		t.Fatalf("want bad_param error, got status=%q err=%+v", res.Status, res.Error)
	}
}

func TestRenderTemplate_ParseError(t *testing.T) {
	res := runRenderTemplate(t, map[string]any{"template": "{{.unclosed"}, nil)
	if res.Status != core.StatusError || res.Error == nil || res.Error.Code != "bad_param" {
		t.Fatalf("want bad_param error, got status=%q err=%+v", res.Status, res.Error)
	}
}

func TestRenderTemplate_NonTextTemplateInput(t *testing.T) {
	res := runRenderTemplate(t, map[string]any{},
		map[string]core.Ref{"template": {Inline: 42}})
	if res.Status != core.StatusError || res.Error == nil || res.Error.Code != "bad_input" {
		t.Fatalf("want bad_input error, got status=%q err=%+v", res.Status, res.Error)
	}
}

func TestRenderTemplate_BadData(t *testing.T) {
	res := runRenderTemplate(t,
		map[string]any{"template": "{{.x}}"},
		map[string]core.Ref{"data": {Inline: 3.14}}, // not an object/array/json
	)
	if res.Status != core.StatusError || res.Error == nil || res.Error.Code != "bad_input" {
		t.Fatalf("want bad_input error, got status=%q err=%+v", res.Status, res.Error)
	}
}

// --- adversarial / security edge cases ---

// TestRenderTemplate_NoSecondOrderInjection is the key SSTI property:
// data is the render CONTEXT, never re-parsed as a template. A data value
// that itself looks like a template action must come out as an escaped
// literal, not get evaluated — otherwise untrusted data (a webhook field)
// could read sibling fields it shouldn't.
func TestRenderTemplate_NoSecondOrderInjection(t *testing.T) {
	got := renderedHTML(t,
		map[string]any{"template": "{{.greeting}}"},
		map[string]core.Ref{"data": {Inline: map[string]any{
			"greeting": "{{.secret}}",
			"secret":   "TOPSECRET",
		}}},
	)
	if strings.Contains(got, "TOPSECRET") {
		t.Fatalf("data was evaluated as a template — secret leaked: %q", got)
	}
	// The literal "{{.secret}}" is rendered, HTML-escaped, not executed.
	if !strings.Contains(got, "{{.secret}}") {
		t.Errorf("expected the literal action text, got %q", got)
	}
}

// TestRenderTemplate_Recursion: an infinitely self-referential template
// must fail with an error, not hang or crash the worker. Go's template
// engine caps execution depth; we assert we surface that as a clean
// error result.
func TestRenderTemplate_Recursion(t *testing.T) {
	res := runRenderTemplate(t,
		map[string]any{"template": `{{define "x"}}{{template "x" .}}{{end}}{{template "x" .}}`},
		map[string]core.Ref{"data": {Inline: map[string]any{}}},
	)
	if res.Status != core.StatusError || res.Error == nil {
		t.Fatalf("recursive template should error, got status=%q err=%+v", res.Status, res.Error)
	}
}

// TestRenderTemplate_MethodCallOnData: calling a method/field that the
// JSON data doesn't have is an author error — it must surface as an error
// result, never a panic (the engine's recover would mask a panic, but the
// drop should fail cleanly on its own).
func TestRenderTemplate_MethodCallOnData(t *testing.T) {
	res := runRenderTemplate(t,
		map[string]any{"template": "{{.name.Nonexistent}}"},
		map[string]core.Ref{"data": {Inline: map[string]any{"name": "plainstring"}}},
	)
	if res.Status != core.StatusError {
		t.Fatalf("method on a string should error, got status=%q (%v)", res.Status, res.Output)
	}
}

// TestRenderTemplate_OutputCapViaExecute drives the real 8 MiB ceiling
// through Execute (not just the limitedWriter unit), proving a template
// that balloons its output is refused rather than allocated unbounded.
func TestRenderTemplate_OutputCapViaExecute(t *testing.T) {
	big := strings.Repeat("A", (8<<20)+1024) // just over the cap
	res := runRenderTemplate(t,
		map[string]any{"template": "{{.big}}"},
		map[string]core.Ref{"data": {Inline: map[string]any{"big": big}}},
	)
	if res.Status != core.StatusError || res.Error == nil || res.Error.Code != "too_large" {
		t.Fatalf("oversized render should be too_large, got status=%q err=%+v", res.Status, res.Error)
	}
}

// TestRenderTemplate_WholeObjectPrint: printing the root object ({{.}})
// must not crash and must escape its contents.
func TestRenderTemplate_WholeObjectPrint(t *testing.T) {
	got := renderedHTML(t,
		map[string]any{"template": "<pre>{{.}}</pre>"},
		map[string]core.Ref{"data": {Inline: map[string]any{"x": "<b>"}}},
	)
	if strings.Contains(got, "<b>") {
		t.Errorf("object print did not escape inner markup: %q", got)
	}
}

// The output-cap unit test moved to internal/htmltmpl (where limitedWriter
// now lives). The cap is still covered here end-to-end via
// TestRenderTemplate_OutputCapViaExecute, which drives the real Execute path.
