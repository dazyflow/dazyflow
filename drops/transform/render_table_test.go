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

// ----- Column headers vs column keys --------------------------------
//
// The editor has always offered "tap a column to rename it". With only a name
// to write, it wrote the new name into `columns` as the KEY — so the header
// read "Customer" and the cells under it came out blank, because no row has a
// field by that name. These pin the two facts apart.

func TestRenderTable_LabelRenamesTheHeaderOnly(t *testing.T) {
	got := renderedTable(t,
		map[string]any{"columns": []any{
			map[string]any{"column": "customer_email", "label": "Customer"},
			map[string]any{"column": "created_at", "label": "Ordered"},
		}},
		[]map[string]any{{"customer_email": "ada@example.com", "created_at": "2026-08-01"}},
		[]string{"customer_email", "created_at"})

	// The header reads the label...
	if !strings.Contains(got, ">Customer<") || !strings.Contains(got, ">Ordered<") {
		t.Errorf("labels not used as headers: %s", got)
	}
	// ...and the cells still come from the named column. This is the assertion
	// that was failing in production: a renamed column rendered empty.
	if !strings.Contains(got, ">ada@example.com<") || !strings.Contains(got, ">2026-08-01<") {
		t.Errorf("renamed column lost its cells: %s", got)
	}
	// The data's own names are gone from the header row.
	if strings.Contains(got, ">customer_email<") || strings.Contains(got, ">created_at<") {
		t.Errorf("raw column name leaked into the header: %s", got)
	}
}

func TestRenderTable_LabelIsOptional(t *testing.T) {
	// {column} with no label, a blank label, and the bare string form must all
	// head the column with the data's own name.
	for _, col := range []any{
		"name",
		map[string]any{"column": "name"},
		map[string]any{"column": "name", "label": ""},
		map[string]any{"column": "name", "label": "   "},
	} {
		got := renderedTable(t, map[string]any{"columns": []any{col}},
			[]map[string]any{{"name": "Ada"}}, []string{"name"})
		if !strings.Contains(got, ">name<") || !strings.Contains(got, ">Ada<") {
			t.Errorf("column %+v: got %s", col, got)
		}
	}
}

func TestRenderTable_LabelsAreEscaped(t *testing.T) {
	got := renderedTable(t,
		map[string]any{"columns": []any{map[string]any{"column": "name", "label": "<b>Who</b>"}}},
		[]map[string]any{{"name": "Ada"}}, []string{"name"})
	if strings.Contains(got, "<b>Who</b>") {
		t.Errorf("label markup was not escaped: %s", got)
	}
	if !strings.Contains(got, "&lt;b&gt;Who&lt;/b&gt;") {
		t.Errorf("escaped label missing: %s", got)
	}
}

func TestRenderTable_TwoColumnsCanShareALabel(t *testing.T) {
	// Nothing about a header row makes duplicate text invalid, and the columns
	// are identified by key, so this must render both — not collapse or error.
	got := renderedTable(t,
		map[string]any{"columns": []any{
			map[string]any{"column": "a", "label": "Value"},
			map[string]any{"column": "b", "label": "Value"},
		}},
		[]map[string]any{{"a": "1", "b": "2"}}, []string{"a", "b"})
	if strings.Count(got, ">Value<") != 2 {
		t.Errorf("want two headers reading Value: %s", got)
	}
	if !strings.Contains(got, ">1<") || !strings.Contains(got, ">2<") {
		t.Errorf("both columns must still render their own cells: %s", got)
	}
}

func TestRenderTable_MixedStringAndObjectColumns(t *testing.T) {
	got := renderedTable(t,
		map[string]any{"columns": []any{
			"name",
			map[string]any{"column": "customer_email", "label": "Customer"},
		}},
		[]map[string]any{{"name": "Ada", "customer_email": "ada@example.com"}},
		[]string{"name", "customer_email"})
	if i, j := strings.Index(got, ">name<"), strings.Index(got, ">Customer<"); i < 0 || j < 0 || i > j {
		t.Errorf("mixed column forms not both honoured in order: %s", got)
	}
	if !strings.Contains(got, ">ada@example.com<") {
		t.Errorf("labelled column lost its cells: %s", got)
	}
}

func TestRenderTable_ColumnWithNoMatchingFieldIsStillAColumn(t *testing.T) {
	// The editor lets a column be added by name before the step has ever run,
	// so a name that matches nothing yet must render as an empty column rather
	// than an error — that is the pre-existing contract.
	got := renderedTable(t, map[string]any{"columns": []any{"name", "not_in_data"}},
		[]map[string]any{{"name": "Ada"}}, []string{"name"})
	if !strings.Contains(got, ">not_in_data<") {
		t.Errorf("unknown column should still head a column: %s", got)
	}
}

func TestRenderTable_BadColumnEntryIsAnError(t *testing.T) {
	for _, cols := range []any{
		[]any{map[string]any{"label": "Customer"}}, // no 'column'
		[]any{map[string]any{"column": ""}},        // blank 'column'
		[]any{42},
		"name", // not a list
	} {
		res := runRenderTable(t, map[string]any{"columns": cols},
			[]map[string]any{{"name": "Ada"}}, []string{"name"})
		if res.Status != core.StatusError || res.Error.Code != "bad_param" {
			t.Errorf("columns=%+v: status=%q code=%q, want bad_param", cols, res.Status, res.Error.Code)
		}
	}
}

func TestRenderTable_EmptyRowsUsesEmptyParam(t *testing.T) {
	got := renderedTable(t, map[string]any{"empty": "No orders today."},
		[]map[string]any{}, []string{"name"})
	if got != "No orders today." {
		t.Errorf("got %q, want the empty fallback verbatim (no <table>)", got)
	}
}

// ----- Table name (title) -------------------------------------------

func TestRenderTable_TitleRendersAsCaption(t *testing.T) {
	got := renderedTable(t, map[string]any{"title": "Open orders"},
		[]map[string]any{{"name": "Ada"}}, []string{"name"})
	if !strings.Contains(got, "Open orders") {
		t.Fatalf("table name missing: %s", got)
	}
	// A caption is only valid — and only stays put — as the table's first
	// child. Anywhere else and the browser moves it.
	openTable := strings.Index(got, "<table")
	caption := strings.Index(got, "<caption")
	thead := strings.Index(got, "<thead")
	if caption < 0 || !(openTable < caption && caption < thead) {
		t.Errorf("caption must sit between <table> and <thead>: %s", got)
	}
}

func TestRenderTable_NoTitleMeansNoCaption(t *testing.T) {
	// Every table built before this param existed must come out byte-identical.
	for _, params := range []map[string]any{
		{},
		{"title": ""},
		{"title": "   "},
	} {
		got := renderedTable(t, params, []map[string]any{{"name": "Ada"}}, []string{"name"})
		if strings.Contains(got, "<caption") {
			t.Errorf("params %+v: unexpected caption: %s", params, got)
		}
	}
}

func TestRenderTable_TitleIsEscaped(t *testing.T) {
	// The name can carry a ${upstream.…} reference, so it can hold tenant data
	// by the time it gets here — the same reason cells are escaped.
	got := renderedTable(t, map[string]any{"title": `<img src=x onerror="alert(1)">`},
		[]map[string]any{{"name": "Ada"}}, []string{"name"})
	if strings.Contains(got, "<img") {
		t.Errorf("table name was not escaped: %s", got)
	}
	if !strings.Contains(got, "&lt;img src=x onerror=&#34;alert(1)&#34;&gt;") {
		t.Errorf("escaped table name missing: %s", got)
	}
}

func TestRenderTable_TitleIgnoredWhenThereAreNoRows(t *testing.T) {
	// A caption over nothing is a heading for a table that isn't there; the
	// `empty` fallback is the whole message in that case.
	got := renderedTable(t, map[string]any{"title": "Open orders", "empty": "No orders today."},
		[]map[string]any{}, []string{"name"})
	if got != "No orders today." {
		t.Errorf("got %q, want the empty fallback verbatim", got)
	}
}

func TestRenderTable_TitleNotAStringIsIgnored(t *testing.T) {
	// paramStringOr's contract: a non-string param falls back rather than
	// rendering "42" or "<nil>" as the table's name.
	got := renderedTable(t, map[string]any{"title": 42},
		[]map[string]any{{"name": "Ada"}}, []string{"name"})
	if strings.Contains(got, "<caption") {
		t.Errorf("unexpected caption from a non-string title: %s", got)
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
