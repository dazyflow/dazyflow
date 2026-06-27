// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package transform

import (
	"reflect"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

func runParseJSON(t *testing.T, in any, params map[string]any) core.Result {
	t.Helper()
	if params == nil {
		params = map[string]any{}
	}
	res, err := executeParseJSON(t.Context(), core.Job{
		ID:     "test",
		Params: params,
		Input:  map[string]core.Ref{"in": {Inline: in}},
	}, nil)
	if err != nil {
		t.Fatalf("executeParseJSON returned error: %v", err)
	}
	return res
}

func rowsOf(t *testing.T, res core.Result) []map[string]any {
	t.Helper()
	if res.Status != core.StatusOK {
		t.Fatalf("status = %v, error = %+v", res.Status, res.Error)
	}
	rows, ok := res.Output["rows"].Inline.([]map[string]any)
	if !ok {
		t.Fatalf("rows output is %T, want []map[string]any", res.Output["rows"].Inline)
	}
	return rows
}

func TestParseJSON_PlainArray(t *testing.T) {
	res := runParseJSON(t, `[{"vendor":"Acme","total":12.5},{"vendor":"Globex","total":3}]`, nil)
	rows := rowsOf(t, res)
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	if rows[0]["vendor"] != "Acme" {
		t.Errorf("row0 vendor = %v, want Acme", rows[0]["vendor"])
	}
}

func TestParseJSON_SingleObjectBecomesOneRow(t *testing.T) {
	res := runParseJSON(t, `{"sku":"123","price":9.99}`, nil)
	rows := rowsOf(t, res)
	if len(rows) != 1 || rows[0]["sku"] != "123" {
		t.Fatalf("got %+v, want one row with sku=123", rows)
	}
}

func TestParseJSON_StripsMarkdownFence(t *testing.T) {
	in := "Here is the data you asked for:\n\n```json\n[{\"a\":1}]\n```\nLet me know if you need more."
	res := runParseJSON(t, in, nil)
	rows := rowsOf(t, res)
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1 (fence + prose should be stripped)", len(rows))
	}
}

func TestParseJSON_StripsSurroundingProseWithoutFence(t *testing.T) {
	res := runParseJSON(t, `The result is [{"a":1},{"a":2}] — done.`, nil)
	rows := rowsOf(t, res)
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
}

func TestParseJSON_Path(t *testing.T) {
	res := runParseJSON(t, `{"data":{"results":[{"id":1},{"id":2},{"id":3}]}}`, map[string]any{"path": "data.results"})
	rows := rowsOf(t, res)
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3", len(rows))
	}
}

func TestParseJSON_AlreadyParsedValuePassesThrough(t *testing.T) {
	in := []any{map[string]any{"x": "y"}}
	res := runParseJSON(t, in, nil)
	rows := rowsOf(t, res)
	if len(rows) != 1 || rows[0]["x"] != "y" {
		t.Fatalf("got %+v, want one row x=y", rows)
	}
}

func TestParseJSON_DerivesHeaders(t *testing.T) {
	res := runParseJSON(t, `[{"b":1,"a":2}]`, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status = %v", res.Status)
	}
	headers := res.Output["rows"].Headers
	if !reflect.DeepEqual(headers, []string{"a", "b"}) {
		t.Errorf("headers = %v, want sorted [a b]", headers)
	}
}

func TestParseJSON_ScalarIsRejected(t *testing.T) {
	res := runParseJSON(t, `42`, nil)
	if res.Status != core.StatusError {
		t.Fatalf("status = %v, want error for a non-tabular scalar", res.Status)
	}
}

func TestParseJSON_MissingInput(t *testing.T) {
	res, err := executeParseJSON(t.Context(), core.Job{ID: "t", Params: map[string]any{}}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Status != core.StatusError {
		t.Fatalf("status = %v, want error when 'in' is absent", res.Status)
	}
}
