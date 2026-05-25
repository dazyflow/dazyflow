package io

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"git.sr.ht/~klahr/hazy-flow/core"
	"github.com/xuri/excelize/v2"
)

// seedXLSX writes a workbook with the given sheets (name → rows) into
// `path` under `root`. Returns the filename for use in test params.
func seedXLSX(t *testing.T, root, path string, sheets map[string][][]string) {
	t.Helper()
	f := excelize.NewFile()
	defer f.Close()

	// excelize creates "Sheet1" by default; replace it if the caller
	// provided their own first sheet, otherwise leave it as-is.
	first := true
	for name, rows := range sheets {
		if first {
			f.SetSheetName("Sheet1", name)
			first = false
		} else {
			if _, err := f.NewSheet(name); err != nil {
				t.Fatalf("new sheet %q: %v", name, err)
			}
		}
		for r, row := range rows {
			for c, val := range row {
				cell, _ := excelize.CoordinatesToCellName(c+1, r+1)
				if err := f.SetCellValue(name, cell, val); err != nil {
					t.Fatalf("set cell: %v", err)
				}
			}
		}
	}
	if err := f.SaveAs(filepath.Join(root, path)); err != nil {
		t.Fatalf("save xlsx: %v", err)
	}
}

func TestExcelRead_OK(t *testing.T) {
	root := t.TempDir()
	seedXLSX(t, root, "data.xlsx", map[string][][]string{
		"Sheet1": {
			{"name", "age", "city"},
			{"Alice", "30", "Stockholm"},
			{"Bob", "25", "Oslo"},
		},
	})

	res, err := executeExcelRead(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"path": "data.xlsx"},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}

	headers := res.Output["headers"].Inline.([]string)
	if got, want := headers, []string{"name", "age", "city"}; !equalSlice(got, want) {
		t.Errorf("headers = %v, want %v", got, want)
	}

	rows := res.Output["rows"].Inline.([]map[string]string)
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	if rows[0]["name"] != "Alice" || rows[0]["age"] != "30" || rows[0]["city"] != "Stockholm" {
		t.Errorf("row 0 = %+v", rows[0])
	}
	if rows[1]["name"] != "Bob" {
		t.Errorf("row 1 = %+v", rows[1])
	}
}

func TestExcelRead_NamedSheet(t *testing.T) {
	root := t.TempDir()
	seedXLSX(t, root, "multi.xlsx", map[string][][]string{
		"Customers": {{"id"}, {"1"}, {"2"}},
		"Orders":    {{"order_id"}, {"A"}, {"B"}, {"C"}},
	})

	// Without specifying a sheet we get whichever excelize lists first;
	// asking for "Orders" specifically must return 3 rows.
	res, _ := executeExcelRead(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"path": "multi.xlsx", "sheet": "Orders"},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	rows := res.Output["rows"].Inline.([]map[string]string)
	if len(rows) != 3 {
		t.Errorf("Orders rows = %d, want 3", len(rows))
	}
}

func TestExcelRead_UnknownSheet(t *testing.T) {
	root := t.TempDir()
	seedXLSX(t, root, "data.xlsx", map[string][][]string{
		"Sheet1": {{"a"}, {"1"}},
	})
	res, _ := executeExcelRead(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"path": "data.xlsx", "sheet": "Missing"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%q, want error/bad_param", res.Status, res.Error.Code)
	}
}

func TestExcelRead_NoHeaders(t *testing.T) {
	root := t.TempDir()
	seedXLSX(t, root, "data.xlsx", map[string][][]string{
		"Sheet1": {
			{"Alice", "30"},
			{"Bob", "25"},
		},
	})
	res, _ := executeExcelRead(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"path": "data.xlsx", "headers": false},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	headers := res.Output["headers"].Inline.([]string)
	if got, want := headers, []string{"col_0", "col_1"}; !equalSlice(got, want) {
		t.Errorf("headers = %v, want %v", got, want)
	}
	rows := res.Output["rows"].Inline.([]map[string]string)
	if len(rows) != 2 || rows[0]["col_0"] != "Alice" || rows[1]["col_1"] != "25" {
		t.Errorf("rows = %+v", rows)
	}
}

func TestExcelRead_Skip(t *testing.T) {
	// Title row + blank row before the real headers — common in
	// hand-curated spreadsheets.
	root := t.TempDir()
	seedXLSX(t, root, "report.xlsx", map[string][][]string{
		"Sheet1": {
			{"Monthly Report"},
			{""},
			{"name", "value"},
			{"foo", "1"},
		},
	})
	res, _ := executeExcelRead(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"path": "report.xlsx", "skip": 2},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	headers := res.Output["headers"].Inline.([]string)
	if got, want := headers, []string{"name", "value"}; !equalSlice(got, want) {
		t.Errorf("headers = %v, want %v", got, want)
	}
	rows := res.Output["rows"].Inline.([]map[string]string)
	if len(rows) != 1 || rows[0]["name"] != "foo" {
		t.Errorf("rows = %+v", rows)
	}
}

func TestExcelRead_ShortRowsPadded(t *testing.T) {
	// Excel trims trailing empty cells; a row of [name=Alice] in a
	// three-column sheet still needs age="" and city="" keys.
	root := t.TempDir()
	seedXLSX(t, root, "data.xlsx", map[string][][]string{
		"Sheet1": {
			{"name", "age", "city"},
			{"Alice"},
		},
	})
	res, _ := executeExcelRead(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"path": "data.xlsx"},
	}, nil)
	rows := res.Output["rows"].Inline.([]map[string]string)
	if rows[0]["name"] != "Alice" || rows[0]["age"] != "" || rows[0]["city"] != "" {
		t.Errorf("row 0 = %+v, want padded zeros", rows[0])
	}
}

// seedTypedXLSX writes a workbook with mixed cell types using
// excelize's typed SetCellValue (int/float/bool/time). Used for the
// typed:true tests; seedXLSX only writes strings via SetCellValue.
func seedTypedXLSX(t *testing.T, root, path string) {
	t.Helper()
	f := excelize.NewFile()
	defer f.Close()
	// Headers as row 1.
	_ = f.SetCellValue("Sheet1", "A1", "name")
	_ = f.SetCellValue("Sheet1", "B1", "age")
	_ = f.SetCellValue("Sheet1", "C1", "score")
	_ = f.SetCellValue("Sheet1", "D1", "active")
	_ = f.SetCellValue("Sheet1", "E1", "joined")
	// Row 2: typed values.
	_ = f.SetCellValue("Sheet1", "A2", "Alice")
	_ = f.SetCellValue("Sheet1", "B2", 30)
	_ = f.SetCellValue("Sheet1", "C2", 9.5)
	_ = f.SetCellValue("Sheet1", "D2", true)
	// Date column — needs both a typed value and a date-format style,
	// otherwise excelize stores it as a plain number and GetCellType
	// reports CellTypeNumber rather than CellTypeDate.
	dateStyle, _ := f.NewStyle(&excelize.Style{NumFmt: 14}) // m/d/yy
	_ = f.SetCellValue("Sheet1", "E2", time.Date(2024, 3, 15, 0, 0, 0, 0, time.UTC))
	_ = f.SetCellStyle("Sheet1", "E2", "E2", dateStyle)
	// Row 3: another row to confirm we walk past the first.
	_ = f.SetCellValue("Sheet1", "A3", "Bob")
	_ = f.SetCellValue("Sheet1", "B3", 25)
	_ = f.SetCellValue("Sheet1", "C3", 7.0)
	_ = f.SetCellValue("Sheet1", "D3", false)
	if err := f.SaveAs(filepath.Join(root, path)); err != nil {
		t.Fatalf("save: %v", err)
	}
}

func TestExcelRead_TypedValues(t *testing.T) {
	root := t.TempDir()
	seedTypedXLSX(t, root, "data.xlsx")
	res, _ := executeExcelRead(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"path": "data.xlsx", "typed": true},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	rows := res.Output["rows"].Inline.([]map[string]any)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	r := rows[0]
	if v, ok := r["age"].(int64); !ok || v != 30 {
		t.Errorf("age = %T %v, want int64(30)", r["age"], r["age"])
	}
	if v, ok := r["score"].(float64); !ok || v != 9.5 {
		t.Errorf("score = %T %v, want float64(9.5)", r["score"], r["score"])
	}
	if v, ok := r["active"].(bool); !ok || v != true {
		t.Errorf("active = %T %v, want bool(true)", r["active"], r["active"])
	}
	// Date may come back as time.Time when excelize detects the
	// number format. If the test environment's excelize doesn't
	// classify the cell as Date, it'll be float64 — accept either,
	// since both are typed.
	switch v := r["joined"].(type) {
	case time.Time:
		if v.Year() != 2024 || v.Month() != 3 || v.Day() != 15 {
			t.Errorf("joined date = %v, want 2024-03-15", v)
		}
	case float64:
		// Excel serial for 2024-03-15 ≈ 45366. Allow ±1 day for
		// floor/ceil quirks.
		if v < 45365 || v > 45367 {
			t.Errorf("joined float = %v, not near serial-date for 2024-03-15", v)
		}
	default:
		t.Errorf("joined = %T %v, want time.Time or float64", r["joined"], r["joined"])
	}
	if r["name"] != "Alice" {
		t.Errorf("name = %v, want Alice (string)", r["name"])
	}
}

func TestExcelRead_TypedDefaultsToStrings(t *testing.T) {
	// typed unset → existing string-shape behavior preserved.
	root := t.TempDir()
	seedTypedXLSX(t, root, "data.xlsx")
	res, _ := executeExcelRead(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"path": "data.xlsx"},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	// The output type matches the untyped path: []map[string]string.
	if _, ok := res.Output["rows"].Inline.([]map[string]string); !ok {
		t.Errorf("untyped rows = %T, want []map[string]string", res.Output["rows"].Inline)
	}
}

func TestExcelRead_TypedEmptyCellsAreNil(t *testing.T) {
	// In typed mode, an empty cell should be nil (→ JSON null), not
	// "" — letting downstream consumers distinguish "no value" from
	// "explicit empty string".
	root := t.TempDir()
	f := excelize.NewFile()
	_ = f.SetCellValue("Sheet1", "A1", "k")
	_ = f.SetCellValue("Sheet1", "B1", "v")
	_ = f.SetCellValue("Sheet1", "A2", "alpha")
	// B2 intentionally left empty.
	_ = f.SaveAs(filepath.Join(root, "data.xlsx"))
	f.Close()

	res, _ := executeExcelRead(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"path": "data.xlsx", "typed": true},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	rows := res.Output["rows"].Inline.([]map[string]any)
	if rows[0]["v"] != nil {
		t.Errorf("empty cell = %T %v, want nil", rows[0]["v"], rows[0]["v"])
	}
}

func TestExcelRead_TypedWithRangeAndSkip(t *testing.T) {
	// Confirm typed mode respects sheet coordinates after clipping +
	// skipping: the cell at (data row 0, col 0) of the output must
	// look up the correct sheet cell for its type.
	root := t.TempDir()
	f := excelize.NewFile()
	// Banner row 1, blank row 2, headers row 3, data row 4.
	_ = f.SetCellValue("Sheet1", "B1", "report")
	_ = f.SetCellValue("Sheet1", "B3", "count")
	_ = f.SetCellValue("Sheet1", "B4", 42) // typed int
	_ = f.SaveAs(filepath.Join(root, "data.xlsx"))
	f.Close()

	res, _ := executeExcelRead(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params: map[string]any{
			"path":  "data.xlsx",
			"range": "B3:B4",
			"typed": true,
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	rows := res.Output["rows"].Inline.([]map[string]any)
	if v, ok := rows[0]["count"].(int64); !ok || v != 42 {
		t.Errorf("count = %T %v, want int64(42) — wrong sheet coords?", rows[0]["count"], rows[0]["count"])
	}
}

func TestIsDateLikeFormat(t *testing.T) {
	// Direct unit test of the classifier. Cheap to add cases; this
	// is the kind of thing where the next "well-actually" edge case
	// is one regex away from breaking, so the table is intentionally
	// dense.
	cases := []struct {
		fmt  string
		want bool
		why  string
	}{
		// Bare date/time formats.
		{"yyyy-mm-dd", true, "ISO-style date"},
		{"d/m/yy", true, "European short date"},
		{"m/d/yyyy", true, "US date"},
		{"hh:mm:ss", true, "time only"},
		{"yyyy-mm-dd hh:mm:ss", true, "datetime"},
		{"mmmm yyyy", true, "month name + year"},
		{"[h]:mm:ss", true, "elapsed hours"},
		{"[mm]:ss.0", true, "elapsed minutes"},

		// Locale / color / condition prefixes shouldn't fool us.
		{"[$-409]yyyy-mm-dd", true, "Excel locale prefix + date"},
		{"[Red]m/d/yyyy", true, "color prefix + date"},
		{"[<100]m/d;[>=100]0", true, "condition with date in first section"},

		// Pure number / currency formats.
		{"0", false, "integer"},
		{"#,##0.00", false, "thousands-separated"},
		{"$#,##0.00", false, "currency"},
		{"0.00%", false, "percent"},
		{"[Red]#,##0;[Black](#,##0)", false, "colored number"},
		{"General", false, "general format (capital G, not date)"},

		// Quoted literals must not trigger.
		{`"year: "0`, false, "literal 'year' inside quotes"},
		{`"d"0`, false, "literal d inside quotes"},
		{`"YMD"0`, false, "literal date chars inside quotes"},

		// Escaped chars must not trigger.
		{`\m\d\y`, false, "all date chars escaped"},
		{`\y0.00`, false, "escaped y in number format"},

		// Sectioned formats — only the first section matters.
		{"0;\"yyyy\"", false, "first section is number, date string in literal"},
		{"yyyy;0", true, "first section is date"},

		// Edge cases.
		{"", false, "empty"},
		{"[Red]", false, "just a color tag"},
		{"[$USD-409]", false, "just a locale tag"},
	}
	for _, c := range cases {
		t.Run(c.fmt, func(t *testing.T) {
			got := isDateLikeFormat(c.fmt)
			if got != c.want {
				t.Errorf("isDateLikeFormat(%q) = %v, want %v (%s)", c.fmt, got, c.want, c.why)
			}
		})
	}
}

func TestExcelRead_TypedCustomNumFmtAsDate(t *testing.T) {
	// End-to-end: a column with a CUSTOM date format (not one of the
	// 14-22/27-36/45-47/50-58/61-63 built-in IDs) should still come
	// back as time.Time when typed=true.
	root := t.TempDir()
	f := excelize.NewFile()
	customDateFmt := "yyyy-mm-dd"
	style, err := f.NewStyle(&excelize.Style{CustomNumFmt: &customDateFmt})
	if err != nil {
		t.Fatalf("style: %v", err)
	}
	_ = f.SetCellValue("Sheet1", "A1", "when")
	_ = f.SetCellValue("Sheet1", "A2", time.Date(2024, 11, 7, 0, 0, 0, 0, time.UTC))
	_ = f.SetCellStyle("Sheet1", "A2", "A2", style)
	if err := f.SaveAs(filepath.Join(root, "data.xlsx")); err != nil {
		t.Fatalf("save: %v", err)
	}
	f.Close()

	res, _ := executeExcelRead(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"path": "data.xlsx", "typed": true},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	rows := res.Output["rows"].Inline.([]map[string]any)
	v, ok := rows[0]["when"].(time.Time)
	if !ok {
		t.Fatalf("when = %T %v, want time.Time", rows[0]["when"], rows[0]["when"])
	}
	if v.Year() != 2024 || v.Month() != 11 || v.Day() != 7 {
		t.Errorf("when = %v, want 2024-11-07", v)
	}
}

func TestExcelRead_TypedCustomCurrencyStaysNumber(t *testing.T) {
	// Negative companion to the date test above: a custom currency
	// format must NOT trigger date conversion. The value should come
	// back as float64.
	root := t.TempDir()
	f := excelize.NewFile()
	customFmt := `[Red]$#,##0.00;[Black]($#,##0.00)`
	style, err := f.NewStyle(&excelize.Style{CustomNumFmt: &customFmt})
	if err != nil {
		t.Fatalf("style: %v", err)
	}
	_ = f.SetCellValue("Sheet1", "A1", "amount")
	_ = f.SetCellValue("Sheet1", "A2", 1234.56)
	_ = f.SetCellStyle("Sheet1", "A2", "A2", style)
	if err := f.SaveAs(filepath.Join(root, "data.xlsx")); err != nil {
		t.Fatalf("save: %v", err)
	}
	f.Close()

	res, _ := executeExcelRead(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"path": "data.xlsx", "typed": true},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	rows := res.Output["rows"].Inline.([]map[string]any)
	if v, ok := rows[0]["amount"].(float64); !ok || v != 1234.56 {
		t.Errorf("amount = %T %v, want float64(1234.56)", rows[0]["amount"], rows[0]["amount"])
	}
}

func TestExcelRead_NotXLSX(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "fake.xlsx"), []byte("not a zip"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	res, _ := executeExcelRead(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"path": "fake.xlsx"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "parse" {
		t.Errorf("status=%q code=%q, want error/parse", res.Status, res.Error.Code)
	}
}

func TestExcelRead_MissingSandbox(t *testing.T) {
	res, _ := executeExcelRead(t.Context(), core.Job{
		Params: map[string]any{"path": "x.xlsx"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "no_sandbox" {
		t.Errorf("status=%q code=%q, want no_sandbox", res.Status, res.Error.Code)
	}
}

func TestExcelRead_Range(t *testing.T) {
	// Title banner in A1, blank A2, headers in row 3, data in 4-5.
	// Asking for B3:C5 should yield two rows with the col-B/C
	// subset as headers — the A column gets clipped away.
	root := t.TempDir()
	seedXLSX(t, root, "data.xlsx", map[string][][]string{
		"Sheet1": {
			{"Monthly report", "", ""},
			{"", "", ""},
			{"meta", "name", "age"},
			{"x", "Alice", "30"},
			{"y", "Bob", "25"},
		},
	})
	res, _ := executeExcelRead(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"path": "data.xlsx", "range": "B3:C5"},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	headers := res.Output["headers"].Inline.([]string)
	if got, want := headers, []string{"name", "age"}; !equalSlice(got, want) {
		t.Errorf("headers = %v, want %v", got, want)
	}
	rows := res.Output["rows"].Inline.([]map[string]string)
	if len(rows) != 2 || rows[0]["name"] != "Alice" || rows[1]["age"] != "25" {
		t.Errorf("rows = %+v", rows)
	}
}

func TestExcelRead_RangeWithSkip(t *testing.T) {
	// skip applies WITHIN the range — so range A1:C10 + skip 2 means
	// "start at row 3 of the rectangle", which is row 3 of the sheet.
	root := t.TempDir()
	seedXLSX(t, root, "data.xlsx", map[string][][]string{
		"Sheet1": {
			{"banner"},
			{""},
			{"name", "age"},
			{"Alice", "30"},
		},
	})
	res, _ := executeExcelRead(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"path": "data.xlsx", "range": "A1:B4", "skip": 2},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	headers := res.Output["headers"].Inline.([]string)
	if !equalSlice(headers, []string{"name", "age"}) {
		t.Errorf("headers = %v", headers)
	}
}

func TestExcelRead_RangeBeyondData(t *testing.T) {
	// Range extends past the actual data — extra cells should be "".
	root := t.TempDir()
	seedXLSX(t, root, "data.xlsx", map[string][][]string{
		"Sheet1": {{"a", "b"}, {"1", "2"}},
	})
	res, _ := executeExcelRead(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"path": "data.xlsx", "range": "A1:D5"},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	headers := res.Output["headers"].Inline.([]string)
	if len(headers) != 4 || headers[0] != "a" || headers[1] != "b" || headers[2] != "" || headers[3] != "" {
		t.Errorf("headers = %v", headers)
	}
	rows := res.Output["rows"].Inline.([]map[string]string)
	if len(rows) != 4 {
		t.Errorf("rows = %d, want 4 (range height - 1 header)", len(rows))
	}
}

func TestExcelRead_BadRange(t *testing.T) {
	root := t.TempDir()
	seedXLSX(t, root, "data.xlsx", map[string][][]string{
		"Sheet1": {{"a"}},
	})
	for _, attempt := range []string{
		"not-a-range",
		"A1",          // no colon
		"D5:A1",       // reversed
		"A:A",         // whole-column form, not supported
		"A1:",         // missing end
		"foo:bar",     // garbage
	} {
		t.Run(attempt, func(t *testing.T) {
			res, _ := executeExcelRead(t.Context(), core.Job{
				WorkspaceRoot: root,
				Params:        map[string]any{"path": "data.xlsx", "range": attempt},
			}, nil)
			if res.Status != core.StatusError || res.Error.Code != "bad_param" {
				t.Errorf("status=%q code=%q, want bad_param for range=%q", res.Status, res.Error.Code, attempt)
			}
		})
	}
}

func TestExcelRead_PathTraversalBlocked(t *testing.T) {
	root := t.TempDir()
	// Plant a real xlsx outside the sandbox to make sure rejection
	// isn't just "file not found".
	outside := filepath.Join(filepath.Dir(root), "leak.xlsx")
	seedXLSX(t, filepath.Dir(root), "leak.xlsx", map[string][][]string{
		"Sheet1": {{"secret"}},
	})
	t.Cleanup(func() { _ = os.Remove(outside) })

	for _, attempt := range []string{"../leak.xlsx", "/etc/passwd", "../../etc/passwd"} {
		t.Run(attempt, func(t *testing.T) {
			res, _ := executeExcelRead(t.Context(), core.Job{
				WorkspaceRoot: root,
				Params:        map[string]any{"path": attempt},
			}, nil)
			if res.Status == core.StatusOK {
				t.Fatalf("read of %q succeeded — sandbox bypassed", attempt)
			}
		})
	}
}

func equalSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// readBackXLSX is the inverse of seedXLSX: open a file we just wrote
// and return one sheet as [][]string for assertion.
func readBackXLSX(t *testing.T, path, sheet string) [][]string {
	t.Helper()
	f, err := excelize.OpenFile(path)
	if err != nil {
		t.Fatalf("open written xlsx: %v", err)
	}
	defer f.Close()
	if sheet == "" {
		sheet = f.GetSheetList()[0]
	}
	rows, err := f.GetRows(sheet)
	if err != nil {
		t.Fatalf("get rows: %v", err)
	}
	return rows
}

func TestExcelWrite_FromNativeRows(t *testing.T) {
	root := t.TempDir()
	res, err := executeExcelWrite(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"path": "out.xlsx"},
		Input: map[string]core.Ref{
			"rows": {Inline: []map[string]any{
				{"name": "Alice", "age": 30},
				{"name": "Bob", "age": 25},
			}},
			"headers": {Inline: []string{"name", "age"}},
		},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if res.Output["out"].Ref != "out.xlsx" {
		t.Errorf("Ref = %q, want out.xlsx", res.Output["out"].Ref)
	}

	rows := readBackXLSX(t, filepath.Join(root, "out.xlsx"), "")
	want := [][]string{
		{"name", "age"},
		{"Alice", "30"},
		{"Bob", "25"},
	}
	if len(rows) != len(want) {
		t.Fatalf("got %d rows, want %d: %+v", len(rows), len(want), rows)
	}
	for i := range want {
		if !equalSlice(rows[i], want[i]) {
			t.Errorf("row %d = %v, want %v", i, rows[i], want[i])
		}
	}
}

func TestExcelWrite_HeadersDerivedSorted(t *testing.T) {
	// No headers input → derive from row keys, sorted alphabetically.
	root := t.TempDir()
	res, _ := executeExcelWrite(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"path": "out.xlsx"},
		Input: map[string]core.Ref{
			"rows": {Inline: []map[string]any{
				{"zebra": "z", "apple": "a", "mango": "m"},
			}},
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	rows := readBackXLSX(t, filepath.Join(root, "out.xlsx"), "")
	if got, want := rows[0], []string{"apple", "mango", "zebra"}; !equalSlice(got, want) {
		t.Errorf("headers = %v, want %v (sorted)", got, want)
	}
	if got, want := rows[1], []string{"a", "m", "z"}; !equalSlice(got, want) {
		t.Errorf("row = %v, want %v", got, want)
	}
}

func TestExcelWrite_RoundTripFromExcelRead(t *testing.T) {
	// excel_read → excel_write must preserve the data shape end-to-end.
	root := t.TempDir()
	seedXLSX(t, root, "in.xlsx", map[string][][]string{
		"Sheet1": {
			{"name", "age"},
			{"Alice", "30"},
			{"Bob", "25"},
		},
	})
	readRes, _ := executeExcelRead(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"path": "in.xlsx"},
	}, nil)
	if readRes.Status != core.StatusOK {
		t.Fatalf("read: %+v", readRes.Error)
	}

	writeRes, _ := executeExcelWrite(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"path": "out.xlsx"},
		Input: map[string]core.Ref{
			"rows":    {Inline: readRes.Output["rows"].Inline},
			"headers": {Inline: readRes.Output["headers"].Inline},
		},
	}, nil)
	if writeRes.Status != core.StatusOK {
		t.Fatalf("write: %+v", writeRes.Error)
	}

	got := readBackXLSX(t, filepath.Join(root, "out.xlsx"), "")
	want := [][]string{{"name", "age"}, {"Alice", "30"}, {"Bob", "25"}}
	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if !equalSlice(got[i], want[i]) {
			t.Errorf("row %d = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestExcelWrite_JSONRoundtripShape(t *testing.T) {
	// Simulate the gRPC/MCP path: Inline arrives as []any of map[string]any
	// and []any of strings, not the native typed slices.
	root := t.TempDir()
	res, _ := executeExcelWrite(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"path": "out.xlsx"},
		Input: map[string]core.Ref{
			"rows": {Inline: []any{
				map[string]any{"k": "v1"},
				map[string]any{"k": "v2"},
			}},
			"headers": {Inline: []any{"k"}},
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	rows := readBackXLSX(t, filepath.Join(root, "out.xlsx"), "")
	if len(rows) != 3 || rows[1][0] != "v1" || rows[2][0] != "v2" {
		t.Errorf("rows = %+v", rows)
	}
}

func TestExcelWrite_NamedSheet(t *testing.T) {
	root := t.TempDir()
	res, _ := executeExcelWrite(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"path": "out.xlsx", "sheet": "Customers"},
		Input: map[string]core.Ref{
			"rows": {Inline: []map[string]any{{"id": "1"}}},
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	f, _ := excelize.OpenFile(filepath.Join(root, "out.xlsx"))
	defer f.Close()
	sheets := f.GetSheetList()
	if len(sheets) != 1 || sheets[0] != "Customers" {
		t.Errorf("sheets = %v, want [Customers]", sheets)
	}
}

func TestExcelWrite_TypedValues(t *testing.T) {
	// Excel stores numbers and booleans differently from strings;
	// verify SetCellValue passes them through to the cell as typed
	// data, not just stringified.
	root := t.TempDir()
	res, _ := executeExcelWrite(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"path": "out.xlsx"},
		Input: map[string]core.Ref{
			"rows": {Inline: []map[string]any{
				{"i": 42, "f": 3.14, "b": true, "s": "hi"},
			}},
			"headers": {Inline: []string{"i", "f", "b", "s"}},
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	f, _ := excelize.OpenFile(filepath.Join(root, "out.xlsx"))
	defer f.Close()
	// excelize string-encodes for GetCellValue, but the underlying
	// cell type is preserved — easier to assert the visible value.
	if v, _ := f.GetCellValue("Sheet1", "A2"); v != "42" {
		t.Errorf("A2 = %q, want 42", v)
	}
	if v, _ := f.GetCellValue("Sheet1", "C2"); v != "TRUE" {
		t.Errorf("C2 = %q, want TRUE", v)
	}
}

func TestExcelWrite_MissingInput(t *testing.T) {
	root := t.TempDir()
	res, _ := executeExcelWrite(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"path": "out.xlsx"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "missing_input" {
		t.Errorf("status=%q code=%q, want missing_input", res.Status, res.Error.Code)
	}
}

func TestExcelWrite_MissingSandbox(t *testing.T) {
	res, _ := executeExcelWrite(t.Context(), core.Job{
		Params: map[string]any{"path": "out.xlsx"},
		Input: map[string]core.Ref{
			"rows": {Inline: []map[string]any{{"a": "b"}}},
		},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "no_sandbox" {
		t.Errorf("status=%q code=%q, want no_sandbox", res.Status, res.Error.Code)
	}
}

func TestExcelWrite_PathTraversalBlocked(t *testing.T) {
	root := t.TempDir()
	for _, attempt := range []string{"../escape.xlsx", "/tmp/abs.xlsx", "../../etc/passwd"} {
		t.Run(attempt, func(t *testing.T) {
			res, _ := executeExcelWrite(t.Context(), core.Job{
				WorkspaceRoot: root,
				Params:        map[string]any{"path": attempt},
				Input: map[string]core.Ref{
					"rows": {Inline: []map[string]any{{"a": "b"}}},
				},
			}, nil)
			if res.Status == core.StatusOK {
				t.Fatalf("write to %q should not have succeeded", attempt)
			}
		})
	}
}

func TestExcelWrite_Mkdirs(t *testing.T) {
	root := t.TempDir()
	res, err := executeExcelWrite(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"path": "nested/deep/out.xlsx", "mkdirs": true},
		Input: map[string]core.Ref{
			"rows": {Inline: []map[string]any{{"a": "1"}}},
		},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if _, err := os.Stat(filepath.Join(root, "nested/deep/out.xlsx")); err != nil {
		t.Errorf("expected file: %v", err)
	}
}

func TestExcelWrite_QuotaExceeded(t *testing.T) {
	root := t.TempDir()
	// A new .xlsx is ~5KB minimum (zip + manifest); cap well under
	// that to force the quota check to fire.
	res, _ := executeExcelWrite(t.Context(), core.Job{
		WorkspaceRoot: root,
		QuotaLimit:    100,
		Params:        map[string]any{"path": "out.xlsx"},
		Input: map[string]core.Ref{
			"rows": {Inline: []map[string]any{{"a": "b"}}},
		},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "quota_exceeded" {
		t.Errorf("status=%q code=%q, want quota_exceeded", res.Status, res.Error.Code)
	}
	if _, err := os.Stat(filepath.Join(root, "out.xlsx")); !os.IsNotExist(err) {
		t.Errorf("blocked write left file on disk: %v", err)
	}
}

func TestExcelWrite_EmptyRows(t *testing.T) {
	// No rows, no headers — should produce a valid empty workbook,
	// not an error.
	root := t.TempDir()
	res, _ := executeExcelWrite(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"path": "empty.xlsx"},
		Input: map[string]core.Ref{
			"rows": {Inline: []map[string]any{}},
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	rows := readBackXLSX(t, filepath.Join(root, "empty.xlsx"), "")
	if len(rows) != 0 {
		t.Errorf("rows = %+v, want empty", rows)
	}
}

func TestExcelWrite_AppendExistingFile(t *testing.T) {
	// Seed a file with 2 data rows, then append 2 more. End state: 4
	// rows under the same header.
	root := t.TempDir()
	seedXLSX(t, root, "out.xlsx", map[string][][]string{
		"Sheet1": {
			{"name", "age"},
			{"Alice", "30"},
			{"Bob", "25"},
		},
	})
	res, _ := executeExcelWrite(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"path": "out.xlsx", "append": true},
		Input: map[string]core.Ref{
			"rows": {Inline: []map[string]any{
				{"name": "Carol", "age": 22},
				{"name": "Dave", "age": 40},
			}},
			"headers": {Inline: []string{"name", "age"}},
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	got := readBackXLSX(t, filepath.Join(root, "out.xlsx"), "")
	want := [][]string{
		{"name", "age"},
		{"Alice", "30"},
		{"Bob", "25"},
		{"Carol", "22"},
		{"Dave", "40"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if !equalSlice(got[i], want[i]) {
			t.Errorf("row %d = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestExcelWrite_AppendUsesFileHeaderOrder(t *testing.T) {
	// File's header order is [name, age]. Input row puts age first
	// in iteration (map order is random anyway). The file's order
	// must win — age:25 lands in column B, not column A.
	root := t.TempDir()
	seedXLSX(t, root, "out.xlsx", map[string][][]string{
		"Sheet1": {{"name", "age"}, {"Alice", "30"}},
	})
	res, _ := executeExcelWrite(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"path": "out.xlsx", "append": true},
		Input: map[string]core.Ref{
			// Note: NO headers input → relying on the file's order.
			"rows": {Inline: []map[string]any{{"age": 25, "name": "Bob"}}},
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	got := readBackXLSX(t, filepath.Join(root, "out.xlsx"), "")
	if len(got) != 3 || got[2][0] != "Bob" || got[2][1] != "25" {
		t.Errorf("appended row landed in wrong columns: %v", got[2])
	}
}

func TestExcelWrite_AppendIgnoresUnknownColumns(t *testing.T) {
	// Input row has a "phone" column that doesn't exist in the file.
	// The phone value should be silently skipped; the row still
	// lands with name & age.
	root := t.TempDir()
	seedXLSX(t, root, "out.xlsx", map[string][][]string{
		"Sheet1": {{"name", "age"}, {"Alice", "30"}},
	})
	res, _ := executeExcelWrite(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"path": "out.xlsx", "append": true},
		Input: map[string]core.Ref{
			"rows": {Inline: []map[string]any{
				{"name": "Bob", "age": 25, "phone": "555-0100"},
			}},
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	got := readBackXLSX(t, filepath.Join(root, "out.xlsx"), "")
	if len(got[0]) != 2 {
		t.Errorf("phone column leaked into output: %v", got[0])
	}
	if got[2][0] != "Bob" || got[2][1] != "25" {
		t.Errorf("appended row = %v, want [Bob 25]", got[2])
	}
}

func TestExcelWrite_AppendFallsThroughWhenFileMissing(t *testing.T) {
	// append=true + no existing file → behave like a normal create.
	// Safe for recurring graphs that occasionally start from scratch.
	root := t.TempDir()
	res, _ := executeExcelWrite(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"path": "fresh.xlsx", "append": true},
		Input: map[string]core.Ref{
			"rows":    {Inline: []map[string]any{{"x": "1"}}},
			"headers": {Inline: []string{"x"}},
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	got := readBackXLSX(t, filepath.Join(root, "fresh.xlsx"), "")
	if len(got) != 2 || got[0][0] != "x" || got[1][0] != "1" {
		t.Errorf("fresh write via append failed: %v", got)
	}
}

func TestExcelWrite_AppendToMissingSheetFallsThrough(t *testing.T) {
	// File exists, but the named sheet doesn't → fall through to a
	// fresh write that REPLACES the file with just the new sheet.
	// (Documented behavior; alternative would be silently adding a
	// new sheet to the existing workbook, which is less visible.)
	root := t.TempDir()
	seedXLSX(t, root, "out.xlsx", map[string][][]string{
		"OtherSheet": {{"a"}, {"1"}},
	})
	res, _ := executeExcelWrite(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"path": "out.xlsx", "append": true, "sheet": "NewSheet"},
		Input: map[string]core.Ref{
			"rows":    {Inline: []map[string]any{{"k": "v"}}},
			"headers": {Inline: []string{"k"}},
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	got := readBackXLSX(t, filepath.Join(root, "out.xlsx"), "NewSheet")
	if len(got) != 2 || got[0][0] != "k" || got[1][0] != "v" {
		t.Errorf("fresh-write fallback failed: %v", got)
	}
}

func TestExcelWrite_AppendQuotaCountsDeltaOnly(t *testing.T) {
	// Seed a file ≈4KB. Tenant usage already includes that. The
	// quota check on append must only count the SIZE DELTA — adding
	// one tiny row should not be rejected just because the existing
	// file was already most of the budget.
	root := t.TempDir()
	seedXLSX(t, root, "out.xlsx", map[string][][]string{
		"Sheet1": {{"k"}, {"v1"}},
	})
	info, _ := os.Stat(filepath.Join(root, "out.xlsx"))
	limit := info.Size() + 4096 // 4KB headroom — far more than one cell adds
	res, _ := executeExcelWrite(t.Context(), core.Job{
		WorkspaceRoot: root,
		QuotaUsed:     info.Size(), // matches the seeded file
		QuotaLimit:    limit,
		Params:        map[string]any{"path": "out.xlsx", "append": true},
		Input: map[string]core.Ref{
			"rows":    {Inline: []map[string]any{{"k": "v2"}}},
			"headers": {Inline: []string{"k"}},
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v (a fresh-write quota check would have rejected this)",
			res.Status, res.Error)
	}
}

func TestExcelWrite_FreezeRow(t *testing.T) {
	// Just verifying the param doesn't error — visual inspection of
	// the panes is out of scope.
	root := t.TempDir()
	res, _ := executeExcelWrite(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"path": "out.xlsx", "freezeRow": float64(1)},
		Input: map[string]core.Ref{
			"rows":    {Inline: []map[string]any{{"a": "1"}}},
			"headers": {Inline: []string{"a"}},
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
}
