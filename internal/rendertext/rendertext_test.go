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
