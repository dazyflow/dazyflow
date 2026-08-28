// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package transform

import (
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

func runRenderText(t *testing.T, params map[string]any, rows any) core.Result {
	t.Helper()
	res, err := executeRenderText(t.Context(), core.Job{
		Params: params,
		Input:  map[string]core.Ref{"rows": {Inline: rows}},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	return res
}

func renderedText(t *testing.T, params map[string]any, rows any) string {
	t.Helper()
	res := runRenderText(t, params, rows)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	s, ok := res.Output["text"].Inline.(string)
	if !ok {
		t.Fatalf("text output is %T, want string", res.Output["text"].Inline)
	}
	if mime := res.Output["text"].MIME; mime != "text/plain" {
		t.Errorf("text MIME = %q, want text/plain", mime)
	}
	return s
}

func TestRenderText_TemplatePerRow(t *testing.T) {
	got := renderedText(t,
		map[string]any{"template": "row.country + ': ' + string(row.orders)"},
		[]map[string]any{
			{"country": "SE", "orders": int64(12)},
			{"country": "NO", "orders": int64(5)},
		})
	want := "SE: 12\nNO: 5"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRenderText_ColumnWithSeparator(t *testing.T) {
	got := renderedText(t,
		map[string]any{"column": "email", "separator": ", "},
		[]map[string]any{{"email": "a@x"}, {"email": "b@x"}, {"email": "c@x"}})
	if got != "a@x, b@x, c@x" {
		t.Errorf("got %q", got)
	}
}

func TestRenderText_PrefixSuffix(t *testing.T) {
	got := renderedText(t,
		map[string]any{"template": "'- ' + row.line", "prefix": "Items:\n", "suffix": "\n(end)"},
		[]map[string]any{{"line": "one"}, {"line": "two"}})
	want := "Items:\n- one\n- two\n(end)"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRenderText_EmptyRowsUsesEmptyParam(t *testing.T) {
	// Zero rows must not apply prefix/suffix — it emits the `empty`
	// fallback verbatim so a sink gets "Nothing to report." rather than
	// an empty message it would reject.
	got := renderedText(t,
		map[string]any{"template": "row.x", "prefix": "P", "suffix": "S", "empty": "Nothing to report."},
		[]map[string]any{})
	if got != "Nothing to report." {
		t.Errorf("got %q, want the empty fallback", got)
	}
}

func TestRenderText_EmptyRowsDefaultsToBlank(t *testing.T) {
	got := renderedText(t, map[string]any{"column": "x"}, []map[string]any{})
	if got != "" {
		t.Errorf("got %q, want empty string", got)
	}
}

func TestRenderText_NonStringCellsStringified(t *testing.T) {
	got := renderedText(t,
		map[string]any{"column": "v"},
		[]map[string]any{
			{"v": int64(42)},
			{"v": true},
			{"v": map[string]any{"a": int64(1)}},
		})
	want := "42\ntrue\n{\"a\":1}"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRenderText_JSONRoundtripShape(t *testing.T) {
	// rows arrive as []any of map[string]any (gRPC/MCP path).
	got := renderedText(t,
		map[string]any{"column": "name"},
		[]any{
			map[string]any{"name": "A"},
			map[string]any{"name": "B"},
		})
	if got != "A\nB" {
		t.Errorf("got %q", got)
	}
}

// --- Error paths -----------------------------------------------------

func TestRenderText_MissingRowsInput(t *testing.T) {
	res, _ := executeRenderText(t.Context(), core.Job{Params: map[string]any{"column": "x"}}, nil)
	if res.Status != core.StatusError || res.Error.Code != "missing_input" {
		t.Errorf("status=%q code=%q, want missing_input", res.Status, res.Error.Code)
	}
}

func TestRenderText_NeitherTemplateNorColumn(t *testing.T) {
	res := runRenderText(t, map[string]any{}, []map[string]any{{"a": "1"}})
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%q, want bad_param", res.Status, res.Error.Code)
	}
}

func TestRenderText_BadTemplateSyntax(t *testing.T) {
	res := runRenderText(t, map[string]any{"template": "row.a +"}, []map[string]any{{"a": "1"}})
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%q, want bad_param", res.Status, res.Error.Code)
	}
}

func TestRenderText_TemplateRuntimeErrorFailsBatch(t *testing.T) {
	res := runRenderText(t, map[string]any{"template": "row.missing.deep"}, []map[string]any{{"a": "1"}})
	if res.Status != core.StatusError || res.Error.Code != "eval" {
		t.Errorf("status=%q code=%q, want eval", res.Status, res.Error.Code)
	}
}
