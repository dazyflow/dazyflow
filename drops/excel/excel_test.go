// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package excel

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/limits"
	"github.com/dazyflow/dazyflow/drops/internal/rows"
	"github.com/xuri/excelize/v2"
)

// makeXLSX writes a small workbook into dir/name and returns the bare name.
func makeXLSX(t *testing.T, dir, name string, rows [][]any) {
	t.Helper()
	f := excelize.NewFile()
	for i, r := range rows {
		cell, _ := excelize.CoordinatesToCellName(1, i+1)
		if err := f.SetSheetRow("Sheet1", cell, &r); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.SaveAs(filepath.Join(dir, name)); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
}

func TestExcelRead_HeadersAndRows(t *testing.T) {
	ws := t.TempDir()
	makeXLSX(t, ws, "data.xlsx", [][]any{
		{"name", "amount"},
		{"Ada", "100"},
		{"Bo", "250"},
	})

	res, err := executeExcelRead(context.Background(), core.Job{
		Params:        map[string]any{"path": "data.xlsx"},
		WorkspaceRoot: ws,
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	// Column order now rides on the rows Ref's Headers (the former separate
	// "headers" output port was removed when row order was folded onto the Ref).
	headers := res.Output["rows"].Headers
	if len(headers) != 2 || headers[0] != "name" {
		t.Errorf("headers = %v", headers)
	}
	rows := res.Output["rows"].Inline.([]any)
	if len(rows) != 2 {
		t.Fatalf("rows = %+v", rows)
	}
	if rows[0].(map[string]any)["name"] != "Ada" || rows[1].(map[string]any)["amount"] != "250" {
		t.Errorf("rows = %+v", rows)
	}
	// The file path is re-emitted so downstream Excel steps can wire it.
	if res.Output["path"].Inline != "data.xlsx" {
		t.Errorf("path = %+v", res.Output["path"].Inline)
	}
}

func TestExcelRead_Typed(t *testing.T) {
	ws := t.TempDir()
	makeXLSX(t, ws, "n.xlsx", [][]any{{"n"}, {42}, {3}})
	res, _ := executeExcelRead(context.Background(), core.Job{
		Params:        map[string]any{"path": "n.xlsx", "typed": true},
		WorkspaceRoot: ws,
	}, nil)
	rows := res.Output["rows"].Inline.([]any)
	if rows[0].(map[string]any)["n"] != int64(42) {
		t.Errorf("typed n = %#v", rows[0].(map[string]any)["n"])
	}
}

func TestExcelRead_Skip(t *testing.T) {
	ws := t.TempDir()
	makeXLSX(t, ws, "s.xlsx", [][]any{{"banner"}, {"name"}, {"Ada"}})
	res, _ := executeExcelRead(context.Background(), core.Job{
		Params:        map[string]any{"path": "s.xlsx", "skip": 1},
		WorkspaceRoot: ws,
	}, nil)
	headers := res.Output["rows"].Headers
	if len(headers) != 1 || headers[0] != "name" {
		t.Errorf("headers after skip = %v", headers)
	}
}

func TestExcelRead_StripsWorkspaceScheme(t *testing.T) {
	ws := t.TempDir()
	makeXLSX(t, ws, "w.xlsx", [][]any{{"a"}, {"1"}})
	res, _ := executeExcelRead(context.Background(), core.Job{
		Params:        map[string]any{"path": "workspace://w.xlsx"},
		WorkspaceRoot: ws,
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
}

func TestExcelRead_MissingPath(t *testing.T) {
	res, _ := executeExcelRead(context.Background(), core.Job{Params: map[string]any{}, WorkspaceRoot: t.TempDir()}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%v", res.Status, res.Error)
	}
}

func TestExcelWrite_RoundTrip(t *testing.T) {
	ws := t.TempDir()
	res, err := executeExcelWrite(context.Background(), core.Job{
		Params:        map[string]any{"path": "out.xlsx", "sheet": "Sales"},
		WorkspaceRoot: ws,
		Input: map[string]core.Ref{
			// Column order now rides on the rows Ref's Headers (the separate
			// "headers" input port was removed when row order was folded on).
			"rows": {Inline: []map[string]any{{"name": "Ada", "amount": "100"}, {"name": "Bo", "amount": "250"}}, Headers: []string{"name", "amount"}},
		},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if res.Output["out"].Ref != "out.xlsx" {
		t.Errorf("out ref = %q", res.Output["out"].Ref)
	}
	// The file path is also emitted as text so it can feed another Excel
	// step's 'path' input.
	if res.Output["path"].Inline != "out.xlsx" {
		t.Errorf("path = %+v", res.Output["path"].Inline)
	}

	f, err := excelize.OpenFile(filepath.Join(ws, "out.xlsx"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if list := f.GetSheetList(); len(list) != 1 || list[0] != "Sales" {
		t.Errorf("sheets = %v, want [Sales]", list)
	}
	rows, _ := f.GetRows("Sales")
	if len(rows) != 3 || rows[0][0] != "name" || rows[1][0] != "Ada" || rows[2][1] != "250" {
		t.Errorf("rows = %+v", rows)
	}
}

func TestExcelWrite_Append(t *testing.T) {
	ws := t.TempDir()
	job := func() core.Job {
		return core.Job{
			Params:        map[string]any{"path": "log.xlsx", "sheet": "Events", "append": true},
			WorkspaceRoot: ws,
			Input: map[string]core.Ref{
				"rows":    {Inline: []map[string]any{{"e": "a"}}},
				"headers": {Inline: []any{"e"}},
			},
		}
	}
	// First write creates header + 1 row.
	if res, _ := executeExcelWrite(context.Background(), job(), nil); res.Status != core.StatusOK {
		t.Fatalf("first write: %+v", res.Error)
	}
	// Second append adds 1 row, no extra header.
	if res, _ := executeExcelWrite(context.Background(), job(), nil); res.Status != core.StatusOK {
		t.Fatalf("append: %+v", res.Error)
	}
	f, _ := excelize.OpenFile(filepath.Join(ws, "log.xlsx"))
	defer f.Close()
	rows, _ := f.GetRows("Events")
	if len(rows) != 3 { // header + 2 data rows
		t.Errorf("rows after append = %+v (want 3)", rows)
	}
}

func TestExcelWrite_WiredPathOverridesParam(t *testing.T) {
	ws := t.TempDir()
	res, _ := executeExcelWrite(context.Background(), core.Job{
		Params:        map[string]any{"path": "ignored.xlsx"},
		WorkspaceRoot: ws,
		Input: map[string]core.Ref{
			"rows": {Inline: []map[string]any{{"a": "1"}}},
			"path": {Inline: "wired.xlsx"},
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if res.Output["out"].Ref != "wired.xlsx" {
		t.Errorf("out ref = %q, want wired.xlsx", res.Output["out"].Ref)
	}
	if _, err := excelize.OpenFile(filepath.Join(ws, "wired.xlsx")); err != nil {
		t.Errorf("wired.xlsx not written: %v", err)
	}
}

func TestExcelWrite_MissingRowsInput(t *testing.T) {
	res, _ := executeExcelWrite(context.Background(), core.Job{
		Params: map[string]any{"path": "x.xlsx"}, WorkspaceRoot: t.TempDir(),
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "missing_input" {
		t.Errorf("status=%q code=%v", res.Status, res.Error)
	}
}

// TestApplyRange covers the cell-range slicer's edge cases directly: normal
// ranges, reversed coordinates (the parser must normalise), out-of-bounds
// columns (padded with ""), rows past the grid (clamped), a single cell, and
// malformed ranges (error).
func TestApplyRange(t *testing.T) {
	grid := [][]string{
		{"a1", "b1", "c1"},
		{"a2", "b2", "c2"},
		{"a3", "b3", "c3"},
	}

	t.Run("normal A1:B2", func(t *testing.T) {
		out, err := applyRange(grid, "A1:B2", 0)
		if err != nil {
			t.Fatal(err)
		}
		want := [][]string{{"a1", "b1"}, {"a2", "b2"}}
		if !equalGrid(out, want) {
			t.Errorf("got %v, want %v", out, want)
		}
	})

	t.Run("reversed B2:A1 normalises to A1:B2", func(t *testing.T) {
		out, err := applyRange(grid, "B2:A1", 0)
		if err != nil {
			t.Fatal(err)
		}
		want := [][]string{{"a1", "b1"}, {"a2", "b2"}}
		if !equalGrid(out, want) {
			t.Errorf("reversed got %v, want %v", out, want)
		}
	})

	t.Run("out-of-bounds column padded with empty", func(t *testing.T) {
		out, err := applyRange(grid, "A1:E1", 0)
		if err != nil {
			t.Fatal(err)
		}
		want := [][]string{{"a1", "b1", "c1", "", ""}}
		if !equalGrid(out, want) {
			t.Errorf("oob col got %v, want %v", out, want)
		}
	})

	t.Run("rows past the grid are clamped", func(t *testing.T) {
		out, err := applyRange(grid, "A1:A99", 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(out) != 3 {
			t.Errorf("got %d rows, want 3 (clamped to grid height)", len(out))
		}
	})

	t.Run("single cell", func(t *testing.T) {
		out, err := applyRange(grid, "B2:B2", 0)
		if err != nil || !equalGrid(out, [][]string{{"b2"}}) {
			t.Errorf("single cell got %v err=%v", out, err)
		}
	})

	for _, bad := range []string{"A1", "A1:B2:C3", "ZZ:99", "notacell:B2"} {
		t.Run("malformed "+bad, func(t *testing.T) {
			if _, err := applyRange(grid, bad, 0); err == nil {
				t.Errorf("applyRange(%q) = nil error, want a range error", bad)
			}
		})
	}
}

func equalGrid(a, b [][]string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if len(a[i]) != len(b[i]) {
			return false
		}
		for j := range a[i] {
			if a[i][j] != b[i][j] {
				return false
			}
		}
	}
	return true
}

// TestCoerce covers the typed-coercion edges: integers, floats, the bool
// spellings, the typed=false passthrough, the empty-string passthrough, and
// the data-loss cases that are easy to forget (a zero-padded "id" becomes an
// int, dropping the padding; a value too big for int64 falls through to float).
func TestCoerce(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		typed bool
		want  any
	}{
		{"typed off → string", "42", false, "42"},
		{"empty → empty string", "", true, ""},
		{"int", "42", true, int64(42)},
		{"negative int", "-7", true, int64(-7)},
		{"float", "3.14", true, 3.14},
		{"scientific float", "1e3", true, float64(1000)},
		{"TRUE", "TRUE", true, true},
		{"lower true", "true", true, true},
		{"FALSE", "FALSE", true, false},
		{"non-numeric stays string", "Ada", true, "Ada"},
		{"zero-padded id loses padding (int)", "007", true, int64(7)},
		{"huge value overflows int64 → float", "99999999999999999999", true, float64(1e20)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := coerce(c.in, c.typed)
			if got != c.want {
				t.Errorf("coerce(%q, %v) = %#v, want %#v", c.in, c.typed, got, c.want)
			}
		})
	}
}

// TestExcelRead_SkipBeyondData: skipping more rows than exist yields an empty
// read, not an error or panic.
func TestExcelRead_SkipBeyondData(t *testing.T) {
	ws := t.TempDir()
	makeXLSX(t, ws, "s.xlsx", [][]any{{"h"}, {"x"}})
	res, _ := executeExcelRead(context.Background(), core.Job{
		Params:        map[string]any{"path": "s.xlsx", "skip": 50},
		WorkspaceRoot: ws,
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if rows := res.Output["rows"].Inline.([]any); len(rows) != 0 {
		t.Errorf("rows after over-skip = %d, want 0", len(rows))
	}
}

// xlsxBytes_Cov builds an in-memory .xlsx and returns its raw bytes, so a
// test can write it through the sandbox or hand corrupt/edge inputs to the
// readers without touching disk via the makeXLSX disk helper.
func xlsxBytes_Cov(t *testing.T, sheet string, rows [][]any) []byte {
	t.Helper()
	f := excelize.NewFile()
	if sheet != "Sheet1" {
		f.NewSheet(sheet)
		_ = f.DeleteSheet("Sheet1")
	}
	for i, r := range rows {
		cell, _ := excelize.CoordinatesToCellName(1, i+1)
		if err := f.SetSheetRow(sheet, cell, &r); err != nil {
			t.Fatal(err)
		}
	}
	buf, err := f.WriteToBuffer()
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	return buf.Bytes()
}

// --- normalizeRows ---------------------------------------------------------

func TestNormalizeRows_Cov(t *testing.T) {
	mapsEqual := func(a, b []map[string]any) bool { return reflect.DeepEqual(a, b) }

	t.Run("nil", func(t *testing.T) {
		got, err := normalizeRows(nil)
		if err != nil || got != nil {
			t.Errorf("got %v err %v, want nil,nil", got, err)
		}
	})
	t.Run("[]map[string]any passthrough", func(t *testing.T) {
		in := []map[string]any{{"a": 1}}
		got, err := normalizeRows(in)
		if err != nil || !mapsEqual(got, in) {
			t.Errorf("got %v err %v", got, err)
		}
	})
	t.Run("[]any of objects", func(t *testing.T) {
		got, err := normalizeRows([]any{map[string]any{"a": 1}, map[string]any{"b": 2}})
		if err != nil {
			t.Fatal(err)
		}
		want := []map[string]any{{"a": 1}, {"b": 2}}
		if !mapsEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})
	t.Run("[]any with non-object errors", func(t *testing.T) {
		_, err := normalizeRows([]any{map[string]any{"a": 1}, 7})
		if err == nil || !strings.Contains(err.Error(), "row 1") {
			t.Errorf("err = %v, want a row-1 type error", err)
		}
	})
	t.Run("single object wraps to slice", func(t *testing.T) {
		got, err := normalizeRows(map[string]any{"a": 1})
		if err != nil || !mapsEqual(got, []map[string]any{{"a": 1}}) {
			t.Errorf("got %v err %v", got, err)
		}
	})
	t.Run("unsupported type errors", func(t *testing.T) {
		_, err := normalizeRows("not rows")
		if err == nil || !strings.Contains(err.Error(), "must be a JSON array") {
			t.Errorf("err = %v", err)
		}
	})
}

// --- cellStr ---------------------------------------------------------------

func TestCellStr_Cov(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"nil", nil, ""},
		{"string passthrough", "hi", "hi"},
		{"int via Sprintf", 42, "42"},
		{"float via Sprintf", 3.5, "3.5"},
		{"bool via Sprintf", true, "true"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := rows.Cell(c.in); got != c.want {
				t.Errorf("rows.Cell(%v) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// --- rangeError.Error (Error 0.0%) -----------------------------------------

func TestRangeError_Error_Cov(t *testing.T) {
	err := errBadRange("Q9")
	if got := err.Error(); !strings.Contains(got, "invalid range") || !strings.Contains(got, "Q9") {
		t.Errorf("Error() = %q", got)
	}
}

// --- executeExcelRead extra paths ------------------------------------------

func TestExcelRead_NoHeaders_Cov(t *testing.T) {
	ws := t.TempDir()
	makeXLSX(t, ws, "arr.xlsx", [][]any{{"a", "b"}, {"1", "2"}})
	res, _ := executeExcelRead(context.Background(), core.Job{
		Params:        map[string]any{"path": "arr.xlsx", "headers": false},
		WorkspaceRoot: ws,
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	rows := res.Output["rows"].Inline.([]any)
	if len(rows) != 2 {
		t.Fatalf("rows = %+v", rows)
	}
	first := rows[0].([]any)
	if len(first) != 2 || first[0] != "a" || first[1] != "b" {
		t.Errorf("first row = %+v", first)
	}
	if len(res.Output["rows"].Headers) != 0 {
		t.Errorf("headers should be empty for header:false")
	}
}

func TestExcelRead_NoHeaders_Typed_Cov(t *testing.T) {
	ws := t.TempDir()
	makeXLSX(t, ws, "arr.xlsx", [][]any{{1, 2}})
	res, _ := executeExcelRead(context.Background(), core.Job{
		Params:        map[string]any{"path": "arr.xlsx", "headers": false, "typed": true},
		WorkspaceRoot: ws,
	}, nil)
	rows := res.Output["rows"].Inline.([]any)
	first := rows[0].([]any)
	if first[0] != int64(1) {
		t.Errorf("typed array cell = %#v, want int64(1)", first[0])
	}
}

func TestExcelRead_EmptySheetWithHeaders_Cov(t *testing.T) {
	ws := t.TempDir()
	// An empty sheet → grid is empty → the headers branch returns empty rows.
	makeXLSX(t, ws, "empty.xlsx", [][]any{})
	res, _ := executeExcelRead(context.Background(), core.Job{
		Params:        map[string]any{"path": "empty.xlsx"},
		WorkspaceRoot: ws,
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if rows := res.Output["rows"].Inline.([]any); len(rows) != 0 {
		t.Errorf("rows = %+v, want empty", rows)
	}
}

func TestExcelRead_WiredPathInput_Cov(t *testing.T) {
	ws := t.TempDir()
	makeXLSX(t, ws, "wired.xlsx", [][]any{{"h"}, {"v"}})
	res, _ := executeExcelRead(context.Background(), core.Job{
		Params:        map[string]any{"path": "ignored.xlsx"},
		WorkspaceRoot: ws,
		Input:         map[string]core.Ref{"path": {Inline: "wired.xlsx"}},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if res.Output["path"].Inline != "wired.xlsx" {
		t.Errorf("path = %+v", res.Output["path"].Inline)
	}
}

func TestExcelRead_NamedSheet_Cov(t *testing.T) {
	ws := t.TempDir()
	data := xlsxBytes_Cov(t, "Data", [][]any{{"h"}, {"v"}})
	if err := os.WriteFile(filepath.Join(ws, "s.xlsx"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Run("existing named sheet", func(t *testing.T) {
		res, _ := executeExcelRead(context.Background(), core.Job{
			Params:        map[string]any{"path": "s.xlsx", "sheet": "Data"},
			WorkspaceRoot: ws,
		}, nil)
		if res.Status != core.StatusOK {
			t.Fatalf("status=%q err=%+v", res.Status, res.Error)
		}
	})
	t.Run("missing named sheet", func(t *testing.T) {
		res, _ := executeExcelRead(context.Background(), core.Job{
			Params:        map[string]any{"path": "s.xlsx", "sheet": "Nope"},
			WorkspaceRoot: ws,
		}, nil)
		if res.Status != core.StatusError || res.Error.Code != "no_sheet" {
			t.Errorf("status=%q code=%v", res.Status, res.Error)
		}
	})
}

func TestExcelRead_BadRange_Cov(t *testing.T) {
	ws := t.TempDir()
	makeXLSX(t, ws, "r.xlsx", [][]any{{"a"}, {"1"}})
	res, _ := executeExcelRead(context.Background(), core.Job{
		Params:        map[string]any{"path": "r.xlsx", "range": "A1"},
		WorkspaceRoot: ws,
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%v", res.Status, res.Error)
	}
}

func TestExcelRead_TooManyRows_Cov(t *testing.T) {
	restore := limits.SetMaxRows(1)
	defer restore()
	ws := t.TempDir()
	makeXLSX(t, ws, "big.xlsx", [][]any{{"h"}, {"1"}, {"2"}})
	res, _ := executeExcelRead(context.Background(), core.Job{
		Params:        map[string]any{"path": "big.xlsx"},
		WorkspaceRoot: ws,
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "too_many_rows" {
		t.Errorf("status=%q code=%v", res.Status, res.Error)
	}
}

func TestExcelRead_BadXLSXBytes_Cov(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "junk.xlsx"), []byte("not a zip"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, _ := executeExcelRead(context.Background(), core.Job{
		Params:        map[string]any{"path": "junk.xlsx"},
		WorkspaceRoot: ws,
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Errorf("status=%q code=%v", res.Status, res.Error)
	}
}

func TestExcelRead_MissingFile_Cov(t *testing.T) {
	res, _ := executeExcelRead(context.Background(), core.Job{
		Params:        map[string]any{"path": "nope.xlsx"},
		WorkspaceRoot: t.TempDir(),
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Errorf("status=%q code=%v", res.Status, res.Error)
	}
}

func TestExcelRead_SandboxEscape_Cov(t *testing.T) {
	res, _ := executeExcelRead(context.Background(), core.Job{
		Params:        map[string]any{"path": "../../etc/passwd"},
		WorkspaceRoot: t.TempDir(),
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Errorf("status=%q code=%v", res.Status, res.Error)
	}
}

// --- executeExcelWrite extra paths -----------------------------------------

func TestExcelWrite_HeadersFromInputRef_Cov(t *testing.T) {
	// in.Headers populated → headers taken from the Ref, deriveHeaders skipped.
	ws := t.TempDir()
	res, _ := executeExcelWrite(context.Background(), core.Job{
		Params:        map[string]any{"path": "h.xlsx"},
		WorkspaceRoot: ws,
		Input: map[string]core.Ref{
			"rows": {Inline: []map[string]any{{"b": "2", "a": "1"}}, Headers: []string{"a", "b"}},
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	f, _ := excelize.OpenFile(filepath.Join(ws, "h.xlsx"))
	defer f.Close()
	rows, _ := f.GetRows("Sheet1")
	if rows[0][0] != "a" || rows[0][1] != "b" {
		t.Errorf("header order = %+v", rows[0])
	}
}

func TestExcelWrite_DerivedHeaders_Cov(t *testing.T) {
	// No Ref headers → deriveHeaders sorts the union of keys.
	ws := t.TempDir()
	res, _ := executeExcelWrite(context.Background(), core.Job{
		Params:        map[string]any{"path": "d.xlsx"},
		WorkspaceRoot: ws,
		Input: map[string]core.Ref{
			"rows": {Inline: []map[string]any{{"zeta": "z", "alpha": "a"}}},
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	f, _ := excelize.OpenFile(filepath.Join(ws, "d.xlsx"))
	defer f.Close()
	rows, _ := f.GetRows("Sheet1")
	if rows[0][0] != "alpha" || rows[0][1] != "zeta" {
		t.Errorf("derived header order = %+v, want sorted", rows[0])
	}
}

func TestExcelWrite_BadRowsInput_Cov(t *testing.T) {
	res, _ := executeExcelWrite(context.Background(), core.Job{
		Params:        map[string]any{"path": "x.xlsx"},
		WorkspaceRoot: t.TempDir(),
		Input:         map[string]core.Ref{"rows": {Inline: "not an array"}},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Errorf("status=%q code=%v", res.Status, res.Error)
	}
}

func TestExcelWrite_MissingPath_Cov(t *testing.T) {
	res, _ := executeExcelWrite(context.Background(), core.Job{
		Params:        map[string]any{},
		WorkspaceRoot: t.TempDir(),
		Input:         map[string]core.Ref{"rows": {Inline: []map[string]any{{"a": "1"}}}},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%v", res.Status, res.Error)
	}
}

func TestExcelWrite_SandboxError_Cov(t *testing.T) {
	// No WorkspaceRoot configured → sandbox.OpenRoot fails at write time,
	// surfacing the "sandbox" error code from writeSandboxFile.
	res, _ := executeExcelWrite(context.Background(), core.Job{
		Params: map[string]any{"path": "x.xlsx"},
		Input:  map[string]core.Ref{"rows": {Inline: []map[string]any{{"a": "1"}}}},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "sandbox" {
		t.Errorf("status=%q code=%v", res.Status, res.Error)
	}
}

// TestExcelWrite_AppendNewSheet_Cov: append mode, file exists, but the target
// sheet is absent → f.NewSheet(sheet) branch + header written.
func TestExcelWrite_AppendNewSheet_Cov(t *testing.T) {
	ws := t.TempDir()
	// Seed a file that has only "Sheet1".
	data := xlsxBytes_Cov(t, "Sheet1", [][]any{{"x"}, {"1"}})
	if err := os.WriteFile(filepath.Join(ws, "a.xlsx"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	res, _ := executeExcelWrite(context.Background(), core.Job{
		Params:        map[string]any{"path": "a.xlsx", "sheet": "Fresh", "append": true},
		WorkspaceRoot: ws,
		Input: map[string]core.Ref{
			"rows": {Inline: []map[string]any{{"e": "v"}}, Headers: []string{"e"}},
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	f, _ := excelize.OpenFile(filepath.Join(ws, "a.xlsx"))
	defer f.Close()
	rows, _ := f.GetRows("Fresh")
	if len(rows) != 2 || rows[0][0] != "e" || rows[1][0] != "v" {
		t.Errorf("Fresh sheet rows = %+v", rows)
	}
}

// TestExcelWrite_AppendMissingFile_Cov: append mode set but the file does not
// yet exist → falls through to the fresh-file branch.
func TestExcelWrite_AppendMissingFile_Cov(t *testing.T) {
	ws := t.TempDir()
	res, _ := executeExcelWrite(context.Background(), core.Job{
		Params:        map[string]any{"path": "new.xlsx", "sheet": "Sheet1", "append": true},
		WorkspaceRoot: ws,
		Input:         map[string]core.Ref{"rows": {Inline: []map[string]any{{"a": "1"}}}},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if _, err := os.Stat(filepath.Join(ws, "new.xlsx")); err != nil {
		t.Errorf("new.xlsx not created: %v", err)
	}
}

// --- helpers: readSandboxFile / writeSandboxFile / sandboxFileExists -------

func TestReadSandboxFile_TooLarge_Cov(t *testing.T) {
	ws := t.TempDir()
	// Write a file one byte over the cap so the size guard trips.
	big := bytes.Repeat([]byte("x"), maxSandboxFileBytes+1)
	if err := os.WriteFile(filepath.Join(ws, "big.bin"), big, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := readSandboxFile(core.Job{WorkspaceRoot: ws}, "big.bin")
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("err = %v, want size-exceeded error", err)
	}
}

func TestReadSandboxFile_Escape_Cov(t *testing.T) {
	_, err := readSandboxFile(core.Job{WorkspaceRoot: t.TempDir()}, "../../etc/passwd")
	if err == nil {
		t.Error("want error for traversal path")
	}
}

func TestReadSandboxFile_NoRoot_Cov(t *testing.T) {
	// No WorkspaceRoot → OpenRoot returns an error before any file access.
	_, err := readSandboxFile(core.Job{}, "x.xlsx")
	if err == nil {
		t.Error("want error when no workspace root configured")
	}
}

func TestWriteSandboxFile_RoundTrip_Cov(t *testing.T) {
	ws := t.TempDir()
	if err := writeSandboxFile(core.Job{WorkspaceRoot: ws}, "w.bin", []byte("hello")); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(ws, "w.bin"))
	if err != nil || string(got) != "hello" {
		t.Errorf("read back = %q err %v", got, err)
	}
}

func TestWriteSandboxFile_Escape_Cov(t *testing.T) {
	err := writeSandboxFile(core.Job{WorkspaceRoot: t.TempDir()}, "../escape.bin", []byte("x"))
	if err == nil {
		t.Error("want error writing outside the root")
	}
}

func TestWriteSandboxFile_NoRoot_Cov(t *testing.T) {
	err := writeSandboxFile(core.Job{}, "x.bin", []byte("x"))
	if err == nil {
		t.Error("want error when no workspace root configured")
	}
}

func TestSandboxFileExists_Cov(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "there.txt"), []byte("y"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Run("present", func(t *testing.T) {
		if !sandboxFileExists(core.Job{WorkspaceRoot: ws}, "there.txt") {
			t.Error("want true for an existing file")
		}
	})
	t.Run("absent", func(t *testing.T) {
		if sandboxFileExists(core.Job{WorkspaceRoot: ws}, "gone.txt") {
			t.Error("want false for a missing file")
		}
	})
	t.Run("no root → false", func(t *testing.T) {
		if sandboxFileExists(core.Job{}, "x.txt") {
			t.Error("want false when OpenRoot fails")
		}
	})
}
