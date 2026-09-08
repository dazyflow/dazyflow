// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package db

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func newMemDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestSQLConn_QueryAndExec(t *testing.T) {
	db := newMemDB(t)
	c := sqlConn{db: db}
	if err := c.exec(t.Context(), `CREATE TABLE t (id INTEGER, name TEXT)`); err != nil {
		t.Fatalf("exec create: %v", err)
	}

	n, err := c.execBatch(t.Context(), `INSERT INTO t (id, name) VALUES (?, ?)`,
		[]string{"id", "name"}, []map[string]any{
			{"id": 1, "name": "a"},
			{"id": 2, "name": "b"},
		}, "insert")
	if err != nil || n != 2 {
		t.Fatalf("execBatch = (%d, %v), want (2, nil)", n, err)
	}

	cols, rows, err := c.query(t.Context(), `SELECT id, name FROM t ORDER BY id`, nil, 0)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(cols) != 2 || len(rows) != 2 || rows[0]["name"] != "a" {
		t.Fatalf("cols=%v rows=%v", cols, rows)
	}
}

func TestSQLConn_QueryLimit(t *testing.T) {
	db := newMemDB(t)
	c := sqlConn{db: db}
	if err := c.exec(t.Context(), `CREATE TABLE t (id INTEGER)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := c.execBatch(t.Context(), `INSERT INTO t (id) VALUES (?)`,
		[]string{"id"}, []map[string]any{{"id": 1}, {"id": 2}, {"id": 3}}, "insert"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, rows, err := c.query(t.Context(), `SELECT id FROM t ORDER BY id`, nil, 2)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("limit not applied: got %d rows, want 2", len(rows))
	}
}

func TestSQLConn_BytesToString(t *testing.T) {
	db := newMemDB(t)
	c := sqlConn{db: db, bytesToString: true}
	if err := c.exec(t.Context(), `CREATE TABLE t (b BLOB)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO t (b) VALUES (?)`, []byte("hello")); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, rows, err := c.query(t.Context(), `SELECT b FROM t`, nil, 0)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if got, ok := rows[0]["b"].(string); !ok || got != "hello" {
		t.Fatalf("b = %#v, want string \"hello\"", rows[0]["b"])
	}
}

func TestSQLConn_QueryError(t *testing.T) {
	c := sqlConn{db: newMemDB(t)}
	if _, _, err := c.query(t.Context(), `SELECT * FROM no_such_table`, nil, 0); err == nil {
		t.Fatal("want error for missing table")
	}
}

func TestSQLConn_ExecBatchPrepareError(t *testing.T) {
	c := sqlConn{db: newMemDB(t)}
	_, err := c.execBatch(t.Context(), `INSERT INTO missing (x) VALUES (?)`,
		[]string{"x"}, []map[string]any{{"x": 1}}, "insert")
	if err == nil {
		t.Fatal("want prepare error for missing table")
	}
}

// Covers the per-row exec-error rollback branch: a NOT NULL violation on the
// second row rolls the whole batch back.
func TestSQLConn_ExecBatchRowError(t *testing.T) {
	db := newMemDB(t)
	c := sqlConn{db: db}
	if err := c.exec(t.Context(), `CREATE TABLE t (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	_, err := c.execBatch(t.Context(), `INSERT INTO t (id) VALUES (?)`,
		[]string{"id"}, []map[string]any{{"id": 1}, {"id": 1}}, "insert")
	if err == nil {
		t.Fatal("want error for duplicate PK")
	}
	_, rows, qerr := c.query(t.Context(), `SELECT id FROM t`, nil, 0)
	if qerr != nil {
		t.Fatalf("verify query: %v", qerr)
	}
	if len(rows) != 0 {
		t.Fatalf("batch should have rolled back, found %d rows", len(rows))
	}
}
