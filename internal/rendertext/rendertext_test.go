// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package rendertext

import (
	"context"
	"errors"
	"testing"
)

func rows(rs ...map[string]any) []map[string]any { return rs }

func TestRender_TableViaTemplate(t *testing.T) {
	spec := Spec{
		Template:  `'<tr><td>' + string(row["rank"]) + '</td><td>' + row["model"] + '</td></tr>'`,
		Separator: "",
		Prefix:    "<table>",
		Suffix:    "</table>",
	}
	got, err := Render(context.Background(), spec,
		rows(map[string]any{"rank": 1, "model": "A"}, map[string]any{"rank": 2, "model": "B"}), 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "<table><tr><td>1</td><td>A</td></tr><tr><td>2</td><td>B</td></tr></table>"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRender_ColumnPathWithSeparator(t *testing.T) {
	spec := Spec{Column: "name", Separator: ", "}
	got, err := Render(context.Background(), spec,
		rows(map[string]any{"name": "a"}, map[string]any{"name": "b"}), 0)
	if err != nil || got != "a, b" {
		t.Fatalf("got %q err %v, want \"a, b\"", got, err)
	}
}

func TestRender_EmptyRowsReturnsEmptyParam(t *testing.T) {
	got, err := Render(context.Background(), Spec{Template: `row["x"]`, Prefix: "P", Empty: "none"}, nil, 0)
	if err != nil || got != "none" {
		t.Fatalf("got %q err %v, want \"none\"", got, err)
	}
}

func TestRender_NoRenderer(t *testing.T) {
	_, err := Render(context.Background(), Spec{}, rows(map[string]any{"x": 1}), 0)
	if !errors.Is(err, ErrNoRenderer) {
		t.Fatalf("err = %v, want ErrNoRenderer", err)
	}
}

func TestRender_BadTemplateIsParseError(t *testing.T) {
	_, err := Render(context.Background(), Spec{Template: `row["x" +`}, rows(map[string]any{"x": 1}), 0)
	var pe *ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("err = %v (%T), want *ParseError", err, err)
	}
}

func TestRender_RuntimeErrorIsEvalError(t *testing.T) {
	// string(null) is a runtime CEL error — must surface as an EvalError so the
	// drop maps it to "eval", not "bad_param".
	_, err := Render(context.Background(), Spec{Template: `string(row["missing"])`}, rows(map[string]any{"x": 1}), 0)
	var ee *EvalError
	if !errors.As(err, &ee) {
		t.Fatalf("err = %v (%T), want *EvalError", err, err)
	}
}

func TestRender_MaxBytesCeiling(t *testing.T) {
	_, err := Render(context.Background(), Spec{Column: "x"}, rows(map[string]any{"x": "0123456789"}), 4)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
}

func TestRender_NullCellViaGuard(t *testing.T) {
	// The generated table CEL guards nulls; confirm the guard pattern renders "".
	spec := Spec{Template: `(row["v"] == null ? "" : string(row["v"]))`}
	got, err := Render(context.Background(), spec, rows(map[string]any{"v": nil}), 0)
	if err != nil || got != "" {
		t.Fatalf("got %q err %v, want empty string", got, err)
	}
}

// TestSpecFromParams covers the shared defaults. The function exists so the
// drop and the editor's live preview cannot drift, and its one subtle rule is
// the separator: absent means newline, but an explicit "" must stay empty —
// that is what the HTML-table preset relies on to join cells with no gap.
// daemon/render_text_preview.go re-implements the same rule against a typed
// JSON body (it has no params map to pass here), so this pins the drop half.
func TestSpecFromParams(t *testing.T) {
	// An empty params map yields the documented defaults.
	got := SpecFromParams(map[string]any{})
	if got.Separator != "\n" {
		t.Errorf("default Separator = %q, want a newline", got.Separator)
	}
	if got != (Spec{Separator: "\n"}) {
		t.Errorf("defaults = %+v, want only Separator set", got)
	}

	// An explicit empty separator survives — it must NOT fall back to newline.
	if s := SpecFromParams(map[string]any{"separator": ""}); s.Separator != "" {
		t.Errorf("explicit empty Separator = %q, want empty (HTML-table preset)", s.Separator)
	}

	// Every field is read through from the params map.
	full := SpecFromParams(map[string]any{
		"template":  `row.name`,
		"column":    "name",
		"separator": ", ",
		"prefix":    "<ul>",
		"suffix":    "</ul>",
		"empty":     "no rows",
	})
	want := Spec{
		Template: `row.name`, Column: "name", Separator: ", ",
		Prefix: "<ul>", Suffix: "</ul>", Empty: "no rows",
	}
	if full != want {
		t.Errorf("SpecFromParams = %+v, want %+v", full, want)
	}

	// A non-string param falls back to the default rather than panicking or
	// stringifying — params arrive from JSON, so a number here is user error.
	nonStr := SpecFromParams(map[string]any{"separator": 42, "prefix": nil})
	if nonStr.Separator != "\n" {
		t.Errorf("non-string Separator = %q, want the newline default", nonStr.Separator)
	}
	if nonStr.Prefix != "" {
		t.Errorf("nil Prefix = %q, want empty", nonStr.Prefix)
	}
}

// TestParseAndEvalErrorUnwrap pins the error wrappers the callers switch on:
// the preview endpoint uses errors.As(&ParseError) to show a template mistake
// inline, and the drop maps ParseError to bad_param and EvalError to eval.
func TestParseAndEvalErrorUnwrap(t *testing.T) {
	inner := errors.New("boom")
	pe := &ParseError{Err: inner}
	if pe.Error() != "boom" || !errors.Is(pe, inner) {
		t.Errorf("ParseError = %q, unwrap ok = %v", pe.Error(), errors.Is(pe, inner))
	}
	ee := &EvalError{Err: inner}
	if ee.Error() != "boom" || !errors.Is(ee, inner) {
		t.Errorf("EvalError = %q, unwrap ok = %v", ee.Error(), errors.Is(ee, inner))
	}
	// The two must stay distinguishable — they map to different drop errors.
	var asParse *ParseError
	if errors.As(error(ee), &asParse) {
		t.Error("an EvalError must not match errors.As(*ParseError)")
	}
}
