// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package transform

import (
	"testing"

	"github.com/dazyflow/dazyflow/core"
)

func runFormat(t *testing.T, p map[string]any, in any) core.Result {
	t.Helper()
	job := core.Job{ID: "t", Params: p}
	if in != nil {
		job.Input = map[string]core.Ref{"in": {Inline: in}}
	}
	res, err := executeFormatText(t.Context(), job, nil)
	if err != nil {
		t.Fatalf("executeFormatText: %v", err)
	}
	return res
}

func formatted(t *testing.T, p map[string]any, in any) string {
	t.Helper()
	res := runFormat(t, p, in)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	s, _ := res.Output["out"].Inline.(string)
	return s
}

func TestFormatText_Cases(t *testing.T) {
	for name, tc := range map[string]struct {
		op, in, want string
	}{
		"upper":                 {"upper", "ada lovelace", "ADA LOVELACE"},
		"lower":                 {"lower", "ADA Lovelace", "ada lovelace"},
		"title lowers first":    {"title", "ADA LOVELACE", "Ada Lovelace"},
		"title keeps unicode":   {"title", "ÅSA ÖBERG", "Åsa Öberg"},
		"sentence":              {"sentence", "ADA WROTE THE NOTES", "Ada wrote the notes"},
		"sentence skips spaces": {"sentence", "   ada", "   Ada"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := formatted(t, map[string]any{"op": tc.op}, tc.in); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// The fix for text pasted out of a web page: both ends AND the middle.
func TestFormatText_TidyCollapsesTheMiddleToo(t *testing.T) {
	if got := formatted(t, map[string]any{"op": "tidy"}, "  Coffee\n\n   beans  "); got != "Coffee beans" {
		t.Errorf("got %q", got)
	}
}

func TestFormatText_ShortenOnlyMarksARealCut(t *testing.T) {
	if got := formatted(t, map[string]any{"op": "shorten", "length": 5}, "short"); got != "short" {
		t.Errorf("got %q — nothing was cut, so nothing should be marked", got)
	}
	if got := formatted(t, map[string]any{"op": "shorten", "length": 6}, "Coffee beans"); got != "Coffee…" {
		t.Errorf("got %q", got)
	}
	// Cutting must land on a character boundary, never inside one.
	if got := formatted(t, map[string]any{"op": "shorten", "length": 3}, "Åsa Öberg"); got != "Åsa…" {
		t.Errorf("got %q", got)
	}
}

func TestFormatText_DefaultOnlyFillsABlank(t *testing.T) {
	if got := formatted(t, map[string]any{"op": "default", "value": "(none)"}, "   "); got != "(none)" {
		t.Errorf("got %q", got)
	}
	if got := formatted(t, map[string]any{"op": "default", "value": "(none)"}, "a subject"); got != "a subject" {
		t.Errorf("got %q — text that was there should pass through", got)
	}
}

// Plainly, not as a pattern: a full stop means a full stop.
func TestFormatText_ReplaceIsLiteral(t *testing.T) {
	if got := formatted(t, map[string]any{"op": "replace", "find": "Ltd.", "replacement": "Limited"}, "Bean Ltd. and Beans Ltdx"); got != "Bean Limited and Beans Ltdx" {
		t.Errorf("got %q", got)
	}
	if got := formatted(t, map[string]any{"op": "replace", "find": " "}, "a b c"); got != "abc" {
		t.Errorf("got %q — an empty replacement removes what was found", got)
	}
}

func TestFormatText_SplitGivesAListAndRows(t *testing.T) {
	res := runFormat(t, map[string]any{"op": "split", "separator": ","}, "a, b ,c")
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	pieces, _ := res.Output["out"].Inline.([]any)
	if len(pieces) != 3 || pieces[1] != "b" {
		t.Errorf("pieces = %v, want the three trimmed", pieces)
	}
	rows, _ := res.Output["rows"].Inline.([]map[string]any)
	if len(rows) != 3 || rows[2]["value"] != "c" {
		t.Errorf("rows = %v", rows)
	}
	if cols := res.Output["rows"].Headers; len(cols) != 1 || cols[0] != "value" {
		t.Errorf("headers = %v", cols)
	}
}

// A separator typed into a text box cannot carry a real newline.
func TestFormatText_SplitOnEscapedNewline(t *testing.T) {
	res := runFormat(t, map[string]any{"op": "split", "separator": "\\n"}, "one\ntwo")
	pieces, _ := res.Output["out"].Inline.([]any)
	if len(pieces) != 2 || pieces[0] != "one" {
		t.Errorf("pieces = %v, want one per line", pieces)
	}
}

func TestFormatText_RejectsWhatCannotWork(t *testing.T) {
	for name, tc := range map[string]struct {
		params map[string]any
		in     any
		code   string
	}{
		"no op":      {map[string]any{}, "x", "bad_param"},
		"unknown op": {map[string]any{"op": "shout"}, "x", "bad_param"},
		"no find":    {map[string]any{"op": "replace"}, "x", "bad_param"},
		"no input":   {map[string]any{"op": "upper"}, nil, "missing_input"},
		"not text":   {map[string]any{"op": "upper"}, []any{1, 2}, "bad_input"},
	} {
		t.Run(name, func(t *testing.T) {
			res := runFormat(t, tc.params, tc.in)
			if res.Status != core.StatusError || res.Error.Code != tc.code {
				t.Fatalf("status=%q err=%+v, want %s", res.Status, res.Error, tc.code)
			}
		})
	}
}

// A number wired in is text as far as this step is concerned.
func TestFormatText_TakesANumber(t *testing.T) {
	if got := formatted(t, map[string]any{"op": "upper"}, float64(42)); got != "42" {
		t.Errorf("got %q", got)
	}
}
