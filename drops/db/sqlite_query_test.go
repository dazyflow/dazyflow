// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package db

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/dazyflow/dazyflow/core"
	_ "modernc.org/sqlite"
)

// seedSqliteQueryDB creates a small mixed-type table for the query
// tests. Uses STRICT mode off so SQLite's permissive typing applies,
// matching what users will get from sqlite_insert_rows by default.
func seedSqliteQueryDB(t *testing.T, root, path string, rows [][]any) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(root, path))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE t (id INTEGER, name TEXT, score REAL, active INTEGER)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	for i, r := range rows {
		if _, err := db.Exec(`INSERT INTO t (id, name, score, active) VALUES (?, ?, ?, ?)`, r...); err != nil {
			t.Fatalf("insert row %d: %v", i, err)
		}
	}
}

func TestSQLiteQuery_NoParams(t *testing.T) {
	root := t.TempDir()
	seedSqliteQueryDB(t, root, "data.db", [][]any{
		{1, "Alice", 9.5, 1},
		{2, "Bob", 7.0, 0},
	})
	res, err := executeSQLiteQuery(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params: map[string]any{
			"path": "data.db",
			"sql":  "SELECT id, name FROM t ORDER BY id",
		},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	cols := res.Output["columns"].Inline.([]string)
	if len(cols) != 2 || cols[0] != "id" || cols[1] != "name" {
		t.Errorf("columns = %v", cols)
	}
	rows := res.Output["rows"].Inline.([]map[string]any)
	if len(rows) != 2 || rows[0]["name"] != "Alice" || rows[1]["name"] != "Bob" {
		t.Errorf("rows = %+v", rows)
	}
}

func TestSQLiteQuery_PositionalParams(t *testing.T) {
	root := t.TempDir()
	seedSqliteQueryDB(t, root, "data.db", [][]any{
		{1, "Alice", 9.5, 1},
		{2, "Bob", 7.0, 0},
		{3, "Carol", 8.5, 1},
	})
	res, _ := executeSQLiteQuery(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params: map[string]any{
			"path":   "data.db",
			"sql":    "SELECT name FROM t WHERE active = ? ORDER BY id",
			"params": []any{1},
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	rows := res.Output["rows"].Inline.([]map[string]any)
	if len(rows) != 2 || rows[0]["name"] != "Alice" || rows[1]["name"] != "Carol" {
		t.Errorf("rows = %+v", rows)
	}
}

func TestSQLiteQuery_TypedValues(t *testing.T) {
	root := t.TempDir()
	seedSqliteQueryDB(t, root, "data.db", [][]any{
		{42, "x", 3.14, 1},
	})
	res, _ := executeSQLiteQuery(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params: map[string]any{
			"path": "data.db",
			"sql":  "SELECT id, score FROM t",
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	row := res.Output["rows"].Inline.([]map[string]any)[0]
	// modernc/sqlite returns INTEGER as int64, REAL as float64.
	if v, ok := row["id"].(int64); !ok || v != 42 {
		t.Errorf("id = %T %v, want int64(42)", row["id"], row["id"])
	}
	if v, ok := row["score"].(float64); !ok || v != 3.14 {
		t.Errorf("score = %T %v, want float64(3.14)", row["score"], row["score"])
	}
}

func TestSQLiteQuery_LimitCaps(t *testing.T) {
	root := t.TempDir()
	seedSqliteQueryDB(t, root, "data.db", [][]any{
		{1, "a", 0.0, 1},
		{2, "b", 0.0, 1},
		{3, "c", 0.0, 1},
		{4, "d", 0.0, 1},
	})
	res, _ := executeSQLiteQuery(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params: map[string]any{
			"path":  "data.db",
			"sql":   "SELECT id FROM t ORDER BY id",
			"limit": 2,
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	rows := res.Output["rows"].Inline.([]map[string]any)
	if len(rows) != 2 {
		t.Errorf("rows = %d, want 2 (limit)", len(rows))
	}
}

func TestSQLiteQuery_NoRows(t *testing.T) {
	root := t.TempDir()
	seedSqliteQueryDB(t, root, "data.db", nil)
	res, _ := executeSQLiteQuery(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params: map[string]any{
			"path": "data.db",
			"sql":  "SELECT id, name FROM t",
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	rows := res.Output["rows"].Inline.([]map[string]any)
	if len(rows) != 0 {
		t.Errorf("rows = %d, want 0", len(rows))
	}
	if cols := res.Output["columns"].Inline.([]string); len(cols) != 2 {
		t.Errorf("columns still emitted from schema: got %v", cols)
	}
}

func TestSQLiteQuery_BadSQL(t *testing.T) {
	root := t.TempDir()
	seedSqliteQueryDB(t, root, "data.db", nil)
	res, _ := executeSQLiteQuery(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params: map[string]any{
			"path": "data.db",
			"sql":  "SELECT nope FROM not_a_real_table",
		},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "db" {
		t.Errorf("status=%q code=%q, want error/db", res.Status, res.Error.Code)
	}
}

func TestSQLiteQuery_MissingPath(t *testing.T) {
	res, _ := executeSQLiteQuery(t.Context(), core.Job{
		WorkspaceRoot: t.TempDir(),
		Params:        map[string]any{"sql": "SELECT 1"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%q, want bad_param", res.Status, res.Error.Code)
	}
}

func TestSQLiteQuery_MissingSQL(t *testing.T) {
	res, _ := executeSQLiteQuery(t.Context(), core.Job{
		WorkspaceRoot: t.TempDir(),
		Params:        map[string]any{"path": "x.db"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%q, want bad_param", res.Status, res.Error.Code)
	}
}

func TestSQLiteQuery_MissingSandbox(t *testing.T) {
	res, _ := executeSQLiteQuery(t.Context(), core.Job{
		Params: map[string]any{"path": "x.db", "sql": "SELECT 1"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "no_sandbox" {
		t.Errorf("status=%q code=%q, want no_sandbox", res.Status, res.Error.Code)
	}
}

func TestSQLiteQuery_PathTraversalBlocked(t *testing.T) {
	root := t.TempDir()
	for _, attempt := range []string{"../escape.db", "/tmp/abs.db", "../../etc/passwd"} {
		t.Run(attempt, func(t *testing.T) {
			res, _ := executeSQLiteQuery(t.Context(), core.Job{
				WorkspaceRoot: root,
				Params:        map[string]any{"path": attempt, "sql": "SELECT 1"},
			}, nil)
			if res.Status == core.StatusOK {
				t.Fatalf("read of %q should have been rejected", attempt)
			}
		})
	}
}

func TestSQLiteQuery_MissingFile(t *testing.T) {
	// File doesn't exist → io error from the sandbox probe.
	root := t.TempDir()
	res, _ := executeSQLiteQuery(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"path": "nonexistent.db", "sql": "SELECT 1"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "io" {
		t.Errorf("status=%q code=%q, want io", res.Status, res.Error.Code)
	}
}

func TestSQLiteQuery_ParamsWrongType(t *testing.T) {
	root := t.TempDir()
	seedSqliteQueryDB(t, root, "data.db", nil)
	res, _ := executeSQLiteQuery(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params: map[string]any{
			"path":   "data.db",
			"sql":    "SELECT 1",
			"params": "not-an-array",
		},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%q, want bad_param", res.Status, res.Error.Code)
	}
}
