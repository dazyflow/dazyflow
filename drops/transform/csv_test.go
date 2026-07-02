// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package transform

import (
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

func runParseCSV(t *testing.T, in any, params map[string]any) core.Result {
	t.Helper()
	if params == nil {
		params = map[string]any{}
	}
	res, err := executeParseCSV(t.Context(), core.Job{
		ID:     "test",
		Params: params,
		Input:  map[string]core.Ref{"in": {Inline: in}},
	}, nil)
	if err != nil {
		t.Fatalf("executeParseCSV returned error: %v", err)
	}
	return res
}

func runBuildCSV(t *testing.T, rows []map[string]any, headers []string, params map[string]any) core.Result {
	t.Helper()
	if params == nil {
		params = map[string]any{}
	}
	res, err := executeBuildCSV(t.Context(), core.Job{
		ID:     "test",
		Params: params,
		Input:  map[string]core.Ref{"rows": {Inline: rows, Headers: headers}},
	}, nil)
	if err != nil {
		t.Fatalf("executeBuildCSV returned error: %v", err)
	}
	return res
}

func csvRowsOf(t *testing.T, res core.Result) ([]map[string]any, []string) {
	t.Helper()
	if res.Status != core.StatusOK {
		t.Fatalf("status = %v, error = %+v", res.Status, res.Error)
	}
	rows, ok := res.Output["rows"].Inline.([]map[string]any)
	if !ok {
		t.Fatalf("rows is %T, want []map[string]any", res.Output["rows"].Inline)
	}
	return rows, res.Output["rows"].Headers
}

func csvTextOf(t *testing.T, res core.Result) string {
	t.Helper()
	if res.Status != core.StatusOK {
		t.Fatalf("status = %v, error = %+v", res.Status, res.Error)
	}
	s, ok := res.Output["out"].Inline.(string)
	if !ok {
		t.Fatalf("out is %T, want string", res.Output["out"].Inline)
	}
	return s
}

func TestParseCSV_HeaderRow(t *testing.T) {
	res := runParseCSV(t, "name,age\nAlice,30\nBob,25", nil)
	rows, headers := csvRowsOf(t, res)
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	if rows[0]["name"] != "Alice" || rows[0]["age"] != "30" {
		t.Errorf("row0 = %+v, want Alice/30", rows[0])
	}
	if len(headers) != 2 || headers[0] != "name" || headers[1] != "age" {
		t.Errorf("headers = %v, want [name age]", headers)
	}
}

func TestParseCSV_NoHeader(t *testing.T) {
	res := runParseCSV(t, "Alice,30\nBob,25", map[string]any{"header": false})
	rows, headers := csvRowsOf(t, res)
	if headers[0] != "col1" || headers[1] != "col2" {
		t.Errorf("headers = %v, want [col1 col2]", headers)
	}
	if rows[0]["col1"] != "Alice" || rows[1]["col2"] != "25" {
		t.Errorf("rows = %+v", rows)
	}
}

func TestParseCSV_TabDelimiter(t *testing.T) {
	res := runParseCSV(t, "name\tage\nAlice\t30", map[string]any{"delimiter": "tab"})
	rows, _ := csvRowsOf(t, res)
	if rows[0]["name"] != "Alice" || rows[0]["age"] != "30" {
		t.Errorf("row0 = %+v, want Alice/30", rows[0])
	}
}

func TestParseCSV_RaggedShortRowPadded(t *testing.T) {
	res := runParseCSV(t, "a,b,c\n1,2", nil)
	rows, _ := csvRowsOf(t, res)
	if rows[0]["c"] != "" {
		t.Errorf("short row should pad c to empty, got %q", rows[0]["c"])
	}
}

func TestParseCSV_QuotedFieldWithComma(t *testing.T) {
	res := runParseCSV(t, "name,note\n\"Doe, Jane\",\"hi, there\"", nil)
	rows, _ := csvRowsOf(t, res)
	if rows[0]["name"] != "Doe, Jane" || rows[0]["note"] != "hi, there" {
		t.Errorf("quoted fields = %+v", rows[0])
	}
}

func TestParseCSV_Empty(t *testing.T) {
	res := runParseCSV(t, "   ", nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Errorf("status/code = %v/%v, want error/bad_input", res.Status, res.Error)
	}
}

func TestParseCSV_NonString(t *testing.T) {
	res := runParseCSV(t, 42, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Errorf("status/code = %v/%v, want error/bad_input", res.Status, res.Error)
	}
}

func TestBuildCSV_HeaderOrderFromRefHeaders(t *testing.T) {
	rows := []map[string]any{
		{"name": "Alice", "age": 30},
		{"name": "Bob", "age": 25},
	}
	res := runBuildCSV(t, rows, []string{"name", "age"}, nil)
	got := csvTextOf(t, res)
	want := "name,age\nAlice,30\nBob,25\n"
	if got != want {
		t.Errorf("csv =\n%q\nwant\n%q", got, want)
	}
}

func TestBuildCSV_ColumnSubsetAndOrder(t *testing.T) {
	rows := []map[string]any{{"name": "Alice", "age": 30, "city": "Malmö"}}
	res := runBuildCSV(t, rows, []string{"name", "age", "city"}, map[string]any{"columns": []any{"city", "name"}})
	got := csvTextOf(t, res)
	want := "city,name\nMalmö,Alice\n"
	if got != want {
		t.Errorf("csv = %q, want %q", got, want)
	}
}

func TestBuildCSV_NoHeader(t *testing.T) {
	rows := []map[string]any{{"a": "1", "b": "2"}}
	res := runBuildCSV(t, rows, []string{"a", "b"}, map[string]any{"header": false})
	got := csvTextOf(t, res)
	if got != "1,2\n" {
		t.Errorf("csv = %q, want \"1,2\\n\"", got)
	}
}

func TestBuildCSV_MissingCellIsBlank(t *testing.T) {
	rows := []map[string]any{{"a": "1"}} // no "b"
	res := runBuildCSV(t, rows, []string{"a", "b"}, nil)
	got := csvTextOf(t, res)
	if got != "a,b\n1,\n" {
		t.Errorf("csv = %q, want \"a,b\\n1,\\n\"", got)
	}
}

func TestBuildCSV_QuotesFieldsNeedingIt(t *testing.T) {
	rows := []map[string]any{{"note": "hi, there"}}
	res := runBuildCSV(t, rows, []string{"note"}, nil)
	got := csvTextOf(t, res)
	want := "note\n\"hi, there\"\n"
	if got != want {
		t.Errorf("csv = %q, want %q", got, want)
	}
}

// Round-trip: parse then build reproduces the canonical CSV.
func TestCSV_RoundTrip(t *testing.T) {
	in := "name,age\nAlice,30\nBob,25"
	parsed := runParseCSV(t, in, nil)
	rows, headers := csvRowsOf(t, parsed)
	built := runBuildCSV(t, rows, headers, nil)
	if got := csvTextOf(t, built); got != in+"\n" {
		t.Errorf("round-trip = %q, want %q", got, in+"\n")
	}
}
