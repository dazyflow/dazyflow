// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package db

import (
	"database/sql"
	"path/filepath"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
	_ "modernc.org/sqlite"
)

// seedUpsertSqliteDB creates a table with id as the conflict key.
func seedUpsertSqliteDB(t *testing.T, root, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(root, path))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT, score REAL)`); err != nil {
		t.Fatalf("create: %v", err)
	}
}

func fetchAllSqlite(t *testing.T, root, path string) []map[string]any {
	t.Helper()
	db := openDB(t, filepath.Join(root, path))
	rows, err := db.Query("SELECT id, name, score FROM t ORDER BY id")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var id int64
		var name string
		var score float64
		if err := rows.Scan(&id, &name, &score); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, map[string]any{"id": id, "name": name, "score": score})
	}
	return out
}

func TestSQLiteUpsert_InsertAndUpdate(t *testing.T) {
	root := t.TempDir()
	seedUpsertSqliteDB(t, root, "data.db")

	// First pass: pure inserts.
	res, _ := executeSQLiteUpsertRows(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params: map[string]any{
			"path":             "data.db",
			"table":            "t",
			"conflict_columns": []string{"id"},
		},
		Input: map[string]core.Ref{
			"rows": {Inline: []map[string]any{
				{"id": 1, "name": "Alice", "score": 9.5},
				{"id": 2, "name": "Bob", "score": 7.0},
			}},
			"headers": {Inline: []string{"id", "name", "score"}},
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("first: status=%q err=%+v", res.Status, res.Error)
	}

	// Second pass: re-insert with new values + one new id.
	res, _ = executeSQLiteUpsertRows(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params: map[string]any{
			"path":             "data.db",
			"table":            "t",
			"conflict_columns": []string{"id"},
		},
		Input: map[string]core.Ref{
			"rows": {Inline: []map[string]any{
				{"id": 1, "name": "Alicia", "score": 10.0},
				{"id": 2, "name": "Bobby", "score": 8.5},
				{"id": 3, "name": "Carol", "score": 9.0},
			}},
			"headers": {Inline: []string{"id", "name", "score"}},
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("second: status=%q err=%+v", res.Status, res.Error)
	}
	if got := res.Output["processed"].Inline; got != 3 {
		t.Errorf("processed = %v, want 3", got)
	}
	all := fetchAllSqlite(t, root, "data.db")
	if len(all) != 3 {
		t.Fatalf("rows = %d, want 3", len(all))
	}
	if all[0]["name"] != "Alicia" || all[1]["name"] != "Bobby" || all[2]["name"] != "Carol" {
		t.Errorf("updates failed: %+v", all)
	}
}

func TestSQLiteUpsert_DoNothingWhenUpdateColsEmpty(t *testing.T) {
	root := t.TempDir()
	seedUpsertSqliteDB(t, root, "data.db")
	_, _ = executeSQLiteUpsertRows(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params: map[string]any{
			"path":             "data.db",
			"table":            "t",
			"conflict_columns": []string{"id"},
		},
		Input: map[string]core.Ref{
			"rows":    {Inline: []map[string]any{{"id": 1, "name": "original", "score": 1.0}}},
			"headers": {Inline: []string{"id", "name", "score"}},
		},
	}, nil)

	res, _ := executeSQLiteUpsertRows(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params: map[string]any{
			"path":             "data.db",
			"table":            "t",
			"conflict_columns": []string{"id"},
			"update_columns":   []string{}, // explicit empty → DO NOTHING
		},
		Input: map[string]core.Ref{
			"rows":    {Inline: []map[string]any{{"id": 1, "name": "should-be-ignored", "score": 99.0}}},
			"headers": {Inline: []string{"id", "name", "score"}},
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	all := fetchAllSqlite(t, root, "data.db")
	if all[0]["name"] != "original" {
		t.Errorf("DO NOTHING failed: name = %v, want 'original'", all[0]["name"])
	}
}

func TestSQLiteUpsert_PartialUpdate(t *testing.T) {
	root := t.TempDir()
	seedUpsertSqliteDB(t, root, "data.db")
	_, _ = executeSQLiteUpsertRows(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params: map[string]any{
			"path":             "data.db",
			"table":            "t",
			"conflict_columns": []string{"id"},
		},
		Input: map[string]core.Ref{
			"rows":    {Inline: []map[string]any{{"id": 1, "name": "orig", "score": 5.0}}},
			"headers": {Inline: []string{"id", "name", "score"}},
		},
	}, nil)

	// Only update name; score should stay at 5.0.
	res, _ := executeSQLiteUpsertRows(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params: map[string]any{
			"path":             "data.db",
			"table":            "t",
			"conflict_columns": []string{"id"},
			"update_columns":   []string{"name"},
		},
		Input: map[string]core.Ref{
			"rows":    {Inline: []map[string]any{{"id": 1, "name": "updated", "score": 99.0}}},
			"headers": {Inline: []string{"id", "name", "score"}},
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	all := fetchAllSqlite(t, root, "data.db")
	if all[0]["name"] != "updated" {
		t.Errorf("name = %v, want 'updated'", all[0]["name"])
	}
	if v, _ := all[0]["score"].(float64); v != 5.0 {
		t.Errorf("score = %v, want preserved at 5.0", all[0]["score"])
	}
}

func TestSQLiteUpsert_CreateTableAddsUnique(t *testing.T) {
	// Without an explicit table, create_table=true should add the
	// UNIQUE constraint; second row with same id should UPDATE not
	// error.
	root := t.TempDir()
	res, _ := executeSQLiteUpsertRows(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params: map[string]any{
			"path":             "data.db",
			"table":            "events",
			"create_table":     true,
			"conflict_columns": []string{"id"},
			"column_types":     map[string]any{"id": "INTEGER", "name": "TEXT"},
		},
		Input: map[string]core.Ref{
			"rows": {Inline: []map[string]any{
				{"id": 1, "name": "a"},
				{"id": 1, "name": "duplicate"},
			}},
			"headers": {Inline: []string{"id", "name"}},
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	db := openDB(t, filepath.Join(root, "data.db"))
	var n int
	var name string
	if err := db.QueryRow(`SELECT count(*), max(name) FROM events`).Scan(&n, &name); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 || name != "duplicate" {
		t.Errorf("rows=%d name=%q, want 1/duplicate", n, name)
	}
}

func TestSQLiteUpsert_RollbackOnFailure(t *testing.T) {
	root := t.TempDir()
	seedUpsertSqliteDB(t, root, "data.db")
	_, _ = executeSQLiteUpsertRows(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params: map[string]any{
			"path":             "data.db",
			"table":            "t",
			"conflict_columns": []string{"id"},
		},
		Input: map[string]core.Ref{
			"rows":    {Inline: []map[string]any{{"id": 1, "name": "baseline", "score": 1.0}}},
			"headers": {Inline: []string{"id", "name", "score"}},
		},
	}, nil)

	res, _ := executeSQLiteUpsertRows(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params: map[string]any{
			"path":             "data.db",
			"table":            "t",
			"conflict_columns": []string{"id"},
		},
		Input: map[string]core.Ref{
			"rows": {Inline: []map[string]any{
				{"id": 1, "name": "would-update", "score": 2.0},
				{"id": 2, "name": "would-insert", "nonexistent": "boom"},
			}},
			"headers": {Inline: []string{"id", "name", "score", "nonexistent"}},
		},
	}, nil)
	if res.Status != core.StatusError {
		t.Fatalf("status=%q, want error", res.Status)
	}
	all := fetchAllSqlite(t, root, "data.db")
	if len(all) != 1 || all[0]["name"] != "baseline" {
		t.Errorf("rollback failed: %+v", all)
	}
}

func TestSQLiteUpsert_MissingConflictColumns(t *testing.T) {
	root := t.TempDir()
	res, _ := executeSQLiteUpsertRows(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"path": "x.db", "table": "t"},
		Input: map[string]core.Ref{
			"rows": {Inline: []map[string]any{{"a": "1"}}},
		},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%q, want bad_param", res.Status, res.Error.Code)
	}
}

func TestSQLiteUpsert_ConflictColumnNotInHeaders(t *testing.T) {
	root := t.TempDir()
	res, _ := executeSQLiteUpsertRows(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params: map[string]any{
			"path":             "x.db",
			"table":            "t",
			"conflict_columns": []string{"id"},
		},
		Input: map[string]core.Ref{
			"rows":    {Inline: []map[string]any{{"a": "1"}}},
			"headers": {Inline: []string{"a"}},
		},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%q, want bad_param", res.Status, res.Error.Code)
	}
}

func TestSQLiteUpsert_PathTraversalBlocked(t *testing.T) {
	root := t.TempDir()
	for _, attempt := range []string{"../escape.db", "/tmp/abs.db"} {
		t.Run(attempt, func(t *testing.T) {
			res, _ := executeSQLiteUpsertRows(t.Context(), core.Job{
				WorkspaceRoot: root,
				Params: map[string]any{
					"path":             attempt,
					"table":            "t",
					"conflict_columns": []string{"id"},
				},
				Input: map[string]core.Ref{
					"rows":    {Inline: []map[string]any{{"id": 1}}},
					"headers": {Inline: []string{"id"}},
				},
			}, nil)
			if res.Status == core.StatusOK {
				t.Fatalf("upsert to %q should have been rejected", attempt)
			}
		})
	}
}

func TestSQLiteUpsert_MissingSandbox(t *testing.T) {
	res, _ := executeSQLiteUpsertRows(t.Context(), core.Job{
		Params: map[string]any{
			"path":             "x.db",
			"table":            "t",
			"conflict_columns": []string{"id"},
		},
		Input: map[string]core.Ref{
			"rows": {Inline: []map[string]any{{"id": 1}}},
		},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "no_sandbox" {
		t.Errorf("status=%q code=%q, want no_sandbox", res.Status, res.Error.Code)
	}
}
