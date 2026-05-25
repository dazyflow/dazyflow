package io

import (
	"os"
	"path/filepath"
	"testing"

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
