// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package transform

import (
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

func runRenderTable(t *testing.T, params map[string]any, rows any, headers []string) core.Result {
	t.Helper()
	ref := core.Ref{Inline: rows}
	if headers != nil {
		ref.Headers = headers
	}
	res, err := executeRenderTable(t.Context(), core.Job{
		Params: params,
		Input:  map[string]core.Ref{"rows": ref},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	return res
}

func renderedTable(t *testing.T, params map[string]any, rows any, headers []string) string {
	t.Helper()
	res := runRenderTable(t, params, rows, headers)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	s, ok := res.Output["html"].Inline.(string)
	if !ok {
		t.Fatalf("html output is %T, want string", res.Output["html"].Inline)
	}
	if mime := res.Output["html"].MIME; mime != "text/html" {
		t.Errorf("html MIME = %q, want text/html", mime)
	}
	return s
}

func TestRenderTable_HeadersAndRows(t *testing.T) {
	got := renderedTable(t, map[string]any{},
		[]map[string]any{
			{"name": "Widget", "qty": int64(2)},
			{"name": "Gadget", "qty": int64(5)},
		},
		[]string{"name", "qty"})

	// Header row uses the raw column names, in the rowset's column order.
	if i, j := strings.Index(got, ">name<"), strings.Index(got, ">qty<"); i < 0 || j < 0 || i > j {
		t.Errorf("headers missing or out of order: %s", got)
	}
	// One header row plus one row per data row.
	if n := strings.Count(got, "<tr>"); n != 3 {
		t.Errorf("<tr> count = %d, want 3 (header + 2 rows)", n)
	}
	for _, want := range []string{">Widget<", ">2<", ">Gadget<", ">5<"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing cell %q in %s", want, got)
		}
	}
}

func TestRenderTable_ColumnsParamSelectsAndOrders(t *testing.T) {
	got := renderedTable(t, map[string]any{"columns": []any{"qty", "name"}},
		[]map[string]any{{"name": "A", "qty": int64(1), "secret": "x"}},
		[]string{"name", "qty", "secret"})

	// Only the listed columns, in the listed order; the dropped column is gone.
	if i, j := strings.Index(got, ">qty<"), strings.Index(got, ">name<"); i < 0 || j < 0 || i > j {
		t.Errorf("columns not reordered to [qty, name]: %s", got)
	}
	if strings.Contains(got, ">secret<") || strings.Contains(got, ">x<") {
		t.Errorf("dropped column leaked: %s", got)
	}
}

func TestRenderTable_EmptyRowsUsesEmptyParam(t *testing.T) {
	got := renderedTable(t, map[string]any{"empty": "No orders today."},
		[]map[string]any{}, []string{"name"})
	if got != "No orders today." {
		t.Errorf("got %q, want the empty fallback verbatim (no <table>)", got)
	}
}

func TestRenderTable_EscapesCellsAndHeaders(t *testing.T) {
	got := renderedTable(t, map[string]any{},
		[]map[string]any{{"a<b": "<script>alert(1)</script>"}},
		[]string{"a<b"})
	if strings.Contains(got, "<script>") {
		t.Errorf("cell markup was not escaped: %s", got)
	}
	if !strings.Contains(got, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Errorf("escaped cell value missing: %s", got)
	}
	if !strings.Contains(got, "a&lt;b") {
		t.Errorf("escaped header missing: %s", got)
	}
}

func TestRenderTable_MissingCellIsBlank(t *testing.T) {
	got := renderedTable(t, map[string]any{},
		[]map[string]any{{"name": "A"}}, // no "email" key
		[]string{"name", "email"})
	// The email cell renders as an empty <td>, not "<nil>".
	if strings.Contains(got, "nil") {
		t.Errorf("nil cell rendered literally: %s", got)
	}
	if c := strings.Count(got, "<td"); c != 2 {
		t.Errorf("<td count = %d, want 2 (one per column)", c)
	}
}

func TestRenderTable_MissingRowsInputErrors(t *testing.T) {
	res, err := executeRenderTable(t.Context(), core.Job{Params: map[string]any{}}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusError || res.Error == nil || res.Error.Code != "missing_input" {
		t.Errorf("want missing_input error, got status=%q err=%+v", res.Status, res.Error)
	}
}
