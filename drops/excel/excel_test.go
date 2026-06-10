package excel

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/xuri/excelize/v2"

	"git.sr.ht/~klahr/hazyflow/core"
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
	headers := res.Output["headers"].Inline.([]string)
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
	headers := res.Output["headers"].Inline.([]string)
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
			"rows":    {Inline: []map[string]any{{"name": "Ada", "amount": "100"}, {"name": "Bo", "amount": "250"}}},
			"headers": {Inline: []any{"name", "amount"}},
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
