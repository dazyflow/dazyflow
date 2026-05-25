package db

import (
	"database/sql"
	"path/filepath"
	"testing"

	"git.sr.ht/~klahr/hazy-flow/core"
	_ "modernc.org/sqlite"
)

// openDB opens the SQLite file we just wrote so tests can verify
// what landed on disk.
func openDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// rowCount is a small helper for the assertion every test ends with.
func rowCount(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

func TestSQLiteInsert_CreateAndInsert(t *testing.T) {
	root := t.TempDir()
	res, err := executeSQLiteInsertRows(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params: map[string]any{
			"path":         "out.db",
			"table":        "customers",
			"create_table": true,
		},
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
	if got, _ := res.Output["inserted"].Inline.(int); got != 2 {
		t.Errorf("inserted = %v, want 2", res.Output["inserted"].Inline)
	}

	db := openDB(t, filepath.Join(root, "out.db"))
	if n := rowCount(t, db, "customers"); n != 2 {
		t.Errorf("row count = %d, want 2", n)
	}
	// Spot-check that the typed value (age:30 → int) made it through
	// SQLite's dynamic typing into the TEXT column we created.
	var name string
	var age string
	if err := db.QueryRow("SELECT name, age FROM customers WHERE name='Alice'").Scan(&name, &age); err != nil {
		t.Fatalf("query: %v", err)
	}
	if name != "Alice" || age != "30" {
		t.Errorf("got name=%q age=%q, want Alice 30", name, age)
	}
}

func TestSQLiteInsert_ColumnTypes(t *testing.T) {
	root := t.TempDir()
	res, _ := executeSQLiteInsertRows(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params: map[string]any{
			"path":         "out.db",
			"table":        "events",
			"create_table": true,
			"column_types": map[string]any{"count": "INTEGER", "name": "TEXT"},
		},
		Input: map[string]core.Ref{
			"rows": {Inline: []map[string]any{{"name": "click", "count": 42}}},
			"headers": {Inline: []string{"name", "count"}},
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	db := openDB(t, filepath.Join(root, "out.db"))
	// Confirm the typed column actually came back as a Go int via
	// sqlite's affinity rules — INTEGER columns return integer values.
	var count int
	if err := db.QueryRow("SELECT count FROM events").Scan(&count); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if count != 42 {
		t.Errorf("count = %d, want 42", count)
	}
}

func TestSQLiteInsert_AppendsToExistingTable(t *testing.T) {
	root := t.TempDir()
	// Pre-create the table so the drop sees it exists; second call
	// without create_table should still insert.
	db := openDB(t, filepath.Join(root, "out.db"))
	if _, err := db.Exec(`CREATE TABLE t (k TEXT)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	db.Close()

	res, _ := executeSQLiteInsertRows(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"path": "out.db", "table": "t"},
		Input: map[string]core.Ref{
			"rows": {Inline: []map[string]any{{"k": "v1"}, {"k": "v2"}}},
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	db = openDB(t, filepath.Join(root, "out.db"))
	if n := rowCount(t, db, "t"); n != 2 {
		t.Errorf("rows = %d, want 2", n)
	}
}

func TestSQLiteInsert_MissingTableNoCreate(t *testing.T) {
	root := t.TempDir()
	res, _ := executeSQLiteInsertRows(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"path": "out.db", "table": "missing"},
		Input: map[string]core.Ref{
			"rows": {Inline: []map[string]any{{"a": "1"}}},
		},
	}, nil)
	// Without create_table, INSERT against a nonexistent table is a
	// SQL error — surface it as a db failure, not a silent no-op.
	if res.Status != core.StatusError || res.Error.Code != "db" {
		t.Errorf("status=%q code=%q, want error/db", res.Status, res.Error.Code)
	}
}

func TestSQLiteInsert_RollbackOnFailure(t *testing.T) {
	// Trying to insert into a non-existent column should fail and
	// leave the table empty — partial inserts violate the contract.
	root := t.TempDir()
	db := openDB(t, filepath.Join(root, "out.db"))
	if _, err := db.Exec(`CREATE TABLE t (a TEXT)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	db.Close()

	res, _ := executeSQLiteInsertRows(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"path": "out.db", "table": "t"},
		Input: map[string]core.Ref{
			// Column "b" doesn't exist on table t.
			"rows":    {Inline: []map[string]any{{"a": "ok"}, {"b": "bad"}}},
			"headers": {Inline: []string{"a", "b"}},
		},
	}, nil)
	if res.Status != core.StatusError {
		t.Fatalf("status=%q, want error", res.Status)
	}
	db = openDB(t, filepath.Join(root, "out.db"))
	if n := rowCount(t, db, "t"); n != 0 {
		t.Errorf("rollback failed: rows = %d, want 0", n)
	}
}

func TestSQLiteInsert_DerivesHeadersAlphabetically(t *testing.T) {
	// No headers input → union of row keys, sorted.
	root := t.TempDir()
	res, _ := executeSQLiteInsertRows(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params: map[string]any{
			"path": "out.db", "table": "t", "create_table": true,
		},
		Input: map[string]core.Ref{
			"rows": {Inline: []map[string]any{{"zebra": "z", "apple": "a"}}},
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	db := openDB(t, filepath.Join(root, "out.db"))
	cols, err := db.Query("PRAGMA table_info(t)")
	if err != nil {
		t.Fatalf("pragma: %v", err)
	}
	defer cols.Close()
	var got []string
	for cols.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if err := cols.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, name)
	}
	if len(got) != 2 || got[0] != "apple" || got[1] != "zebra" {
		t.Errorf("columns = %v, want [apple zebra]", got)
	}
}

func TestSQLiteInsert_JSONRoundtripShape(t *testing.T) {
	// Simulate gRPC/MCP roundtrip: Inline arrives as []any of
	// map[string]any.
	root := t.TempDir()
	res, _ := executeSQLiteInsertRows(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params: map[string]any{
			"path": "out.db", "table": "t", "create_table": true,
		},
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
	db := openDB(t, filepath.Join(root, "out.db"))
	if n := rowCount(t, db, "t"); n != 2 {
		t.Errorf("rows = %d, want 2", n)
	}
}

func TestSQLiteInsert_EmptyRows(t *testing.T) {
	// Zero rows + create_table=true should still create the table
	// (handy for "ensure schema" graph patterns) and report 0 inserted.
	root := t.TempDir()
	res, _ := executeSQLiteInsertRows(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params: map[string]any{
			"path": "out.db", "table": "t", "create_table": true,
		},
		Input: map[string]core.Ref{
			"rows":    {Inline: []map[string]any{}},
			"headers": {Inline: []string{"a", "b"}},
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if got := res.Output["inserted"].Inline; got != 0 {
		t.Errorf("inserted = %v, want 0", got)
	}
	db := openDB(t, filepath.Join(root, "out.db"))
	if _, err := db.Exec("SELECT a, b FROM t"); err != nil {
		t.Errorf("expected table to exist with cols a,b: %v", err)
	}
}

func TestSQLiteInsert_PathTraversalBlocked(t *testing.T) {
	root := t.TempDir()
	for _, attempt := range []string{"../escape.db", "/tmp/abs.db", "../../etc/passwd"} {
		t.Run(attempt, func(t *testing.T) {
			res, _ := executeSQLiteInsertRows(t.Context(), core.Job{
				WorkspaceRoot: root,
				Params:        map[string]any{"path": attempt, "table": "t"},
				Input: map[string]core.Ref{
					"rows": {Inline: []map[string]any{{"a": "1"}}},
				},
			}, nil)
			if res.Status == core.StatusOK {
				t.Fatalf("write to %q should have been rejected", attempt)
			}
		})
	}
}

func TestSQLiteInsert_RejectsUnsafeTableName(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"t; DROP TABLE x", "with space", "with-dash", ""} {
		t.Run(name, func(t *testing.T) {
			res, _ := executeSQLiteInsertRows(t.Context(), core.Job{
				WorkspaceRoot: root,
				Params:        map[string]any{"path": "out.db", "table": name},
				Input: map[string]core.Ref{
					"rows": {Inline: []map[string]any{{"a": "1"}}},
				},
			}, nil)
			if res.Status != core.StatusError || res.Error.Code != "bad_param" {
				t.Errorf("status=%q code=%q, want bad_param", res.Status, res.Error.Code)
			}
		})
	}
}

func TestSQLiteInsert_RejectsUnsafeColumnName(t *testing.T) {
	root := t.TempDir()
	res, _ := executeSQLiteInsertRows(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params: map[string]any{
			"path": "out.db", "table": "t", "create_table": true,
		},
		Input: map[string]core.Ref{
			"rows":    {Inline: []map[string]any{{"weird col": "x"}}},
			"headers": {Inline: []string{"weird col"}},
		},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Errorf("status=%q code=%q, want bad_input", res.Status, res.Error.Code)
	}
}

func TestSQLiteInsert_MissingSandbox(t *testing.T) {
	res, _ := executeSQLiteInsertRows(t.Context(), core.Job{
		Params: map[string]any{"path": "out.db", "table": "t"},
		Input: map[string]core.Ref{
			"rows": {Inline: []map[string]any{{"a": "b"}}},
		},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "no_sandbox" {
		t.Errorf("status=%q code=%q, want no_sandbox", res.Status, res.Error.Code)
	}
}

func TestSQLiteInsert_MissingRowsInput(t *testing.T) {
	root := t.TempDir()
	res, _ := executeSQLiteInsertRows(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"path": "out.db", "table": "t"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "missing_input" {
		t.Errorf("status=%q code=%q, want missing_input", res.Status, res.Error.Code)
	}
}

func TestSQLiteInsert_MkdirsSubdirectory(t *testing.T) {
	// path with directories should be created under the sandbox.
	root := t.TempDir()
	res, _ := executeSQLiteInsertRows(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params: map[string]any{
			"path": "imports/2026/q1/data.db", "table": "t", "create_table": true,
		},
		Input: map[string]core.Ref{
			"rows":    {Inline: []map[string]any{{"a": "1"}}},
			"headers": {Inline: []string{"a"}},
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	db := openDB(t, filepath.Join(root, "imports/2026/q1/data.db"))
	if n := rowCount(t, db, "t"); n != 1 {
		t.Errorf("rows = %d", n)
	}
}
