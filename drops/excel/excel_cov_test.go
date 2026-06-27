// SPDX-FileCopyrightText: 2026 Joachim Klahr
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

	"github.com/xuri/excelize/v2"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/limits"
)

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

// --- normalizeHeaders ------------------------------------------------------

func TestNormalizeHeaders_Cov(t *testing.T) {
	t.Run("[]string passthrough", func(t *testing.T) {
		got := normalizeHeaders([]string{"a", "b"})
		if !reflect.DeepEqual(got, []string{"a", "b"}) {
			t.Errorf("got %v", got)
		}
	})
	t.Run("[]any coerced via cellStr", func(t *testing.T) {
		got := normalizeHeaders([]any{"a", 2, nil, true})
		want := []string{"a", "2", "", "true"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})
	t.Run("unsupported returns nil", func(t *testing.T) {
		if got := normalizeHeaders(map[string]any{}); got != nil {
			t.Errorf("got %v, want nil", got)
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
			if got := cellStr(c.in); got != c.want {
				t.Errorf("cellStr(%v) = %q, want %q", c.in, got, c.want)
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
