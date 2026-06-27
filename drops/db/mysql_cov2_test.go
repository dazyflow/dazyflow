// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package db

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/limits"
	_ "github.com/go-sql-driver/mysql"
)

// mysql_cov2_test.go is the second-round coverage push for the MySQL
// execute paths and the database/sql conn/registry branches that only
// become reachable with a live MySQL server. All tests are gated on
// DAZYFLOW_TEST_MYSQL via the shared mysqlTestSetup helper (mysql_test.go).

// ----------------------------------------------------------------------
// Registry: live-connection branches in sqlDB / sweepLocked.
// ----------------------------------------------------------------------

// TestSQLDBRegistry_LiveConnectAndCacheHit covers the success path of
// sqlDB (sql.Open + PingContext + store) and the cache-hit reuse branch:
// a second call with the same (tenant, dsn) returns the same handle
// without opening a new one.
func TestSQLDBRegistry_LiveConnectAndCacheHit(t *testing.T) {
	dsn, _ := mysqlTestSetup(t)
	r := newSQLDBRegistry("mysql", time.Hour, time.Hour)

	db1, err := r.sqlDB(t.Context(), "acme", dsn)
	if err != nil {
		t.Fatalf("first connect: %v", err)
	}
	if db1 == nil {
		t.Fatal("first connect returned nil db")
	}
	if len(r.dbs) != 1 {
		t.Fatalf("registry size = %d, want 1 after first connect", len(r.dbs))
	}

	db2, err := r.sqlDB(t.Context(), "acme", dsn)
	if err != nil {
		t.Fatalf("cache-hit connect: %v", err)
	}
	if db2 != db1 {
		t.Error("cache hit returned a different *sql.DB handle")
	}
	if len(r.dbs) != 1 {
		t.Errorf("registry size = %d, want 1 (cache hit must not add)", len(r.dbs))
	}

	// Clean up the live handle the registry opened.
	r.mu.Lock()
	for _, e := range r.dbs {
		if e.db != nil {
			_ = e.db.Close()
		}
	}
	r.mu.Unlock()
}

// TestSQLDBRegistry_PingFailureClosesHandle covers the PingContext-error
// branch: a parseable TCP DSN whose host/port nothing listens on passes
// the DSN parse + SSRF pre-flight (private egress is on for the test
// process) but fails the Ping, so sqlDB closes the handle and returns the
// error without poisoning the registry.
func TestSQLDBRegistry_PingFailureClosesHandle(t *testing.T) {
	// Gate on DAZYFLOW_TEST_MYSQL so this only runs where the MySQL
	// paths are exercised, matching the rest of this file.
	mysqlTestSetup(t)

	r := newSQLDBRegistry("mysql", time.Hour, time.Hour)
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	_, err := r.sqlDB(ctx, "acme", "user:pass@tcp(127.0.0.1:1)/db?timeout=2s")
	if err == nil {
		t.Fatal("expected ping error for dead port")
	}
	if len(r.dbs) != 0 {
		t.Errorf("ping failure poisoned registry: %d entries", len(r.dbs))
	}
}

// TestSQLDBRegistry_SweepClosesLiveHandle covers the sweep branch that
// closes a non-nil *sql.DB (conns.go:300-302). We connect for real, then
// backdate the entry past the idle window and sweep.
func TestSQLDBRegistry_SweepClosesLiveHandle(t *testing.T) {
	dsn, _ := mysqlTestSetup(t)
	r := newSQLDBRegistry("mysql", 10*time.Millisecond, 0)

	db, err := r.sqlDB(t.Context(), "acme", dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if db == nil {
		t.Fatal("nil db")
	}

	// Backdate so the entry is past the idle window, then sweep — this
	// exercises the d.Close() on a real handle.
	r.mu.Lock()
	for k := range r.dbs {
		r.dbs[k].lastUse = time.Now().Add(-time.Hour)
	}
	r.sweepLocked(time.Now())
	n := len(r.dbs)
	r.mu.Unlock()

	if n != 0 {
		t.Errorf("registry size = %d after sweep, want 0", n)
	}
	// The handle should now be closed: a query must fail.
	if err := db.PingContext(t.Context()); err == nil {
		t.Error("expected ping to fail on a swept (closed) handle")
	}
}

// ----------------------------------------------------------------------
// Query: too_many_rows guard over a real MySQL connection.
// ----------------------------------------------------------------------

// TestMySQLQuery_TooManyRows covers the queryGuard ceiling stop in
// sqlConn.query (conn_sql.go:64-66) and the too_many_rows error mapping
// in runQueryParsed, driven through a live MySQL SELECT with the row
// ceiling lowered.
func TestMySQLQuery_TooManyRows(t *testing.T) {
	dsn, table := mysqlTestSetup(t)
	ctx := t.Context()
	db, _ := sql.Open("mysql", dsn)
	defer db.Close()
	if _, err := db.ExecContext(ctx, fmt.Sprintf("CREATE TABLE `%s` (id INT)", table)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	for i := 0; i < 5; i++ {
		if _, err := db.ExecContext(ctx, fmt.Sprintf("INSERT INTO `%s` VALUES (?)", table), i); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	restore := limits.SetMaxRows(2)
	defer restore()

	res, _ := executeMySQLQuery(ctx, core.Job{
		Params: map[string]any{
			"dsn": dsn,
			"sql": fmt.Sprintf("SELECT id FROM `%s`", table),
		},
	}, nil)
	if res.Status != core.StatusError || res.Error == nil || res.Error.Code != "too_many_rows" {
		t.Fatalf("status=%q err=%+v, want too_many_rows", res.Status, res.Error)
	}
}

// TestMySQLQuery_UserLimitStop covers the user-limit stop branch of
// queryGuard (limit>0) over a live MySQL connection: more rows exist than
// the limit, so iteration stops early and exactly `limit` rows come back.
func TestMySQLQuery_UserLimitStop(t *testing.T) {
	dsn, table := mysqlTestSetup(t)
	ctx := t.Context()
	db, _ := sql.Open("mysql", dsn)
	defer db.Close()
	if _, err := db.ExecContext(ctx, fmt.Sprintf("CREATE TABLE `%s` (id INT)", table)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	for i := 0; i < 6; i++ {
		if _, err := db.ExecContext(ctx, fmt.Sprintf("INSERT INTO `%s` VALUES (?)", table), i); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	res, _ := executeMySQLQuery(ctx, core.Job{
		Params: map[string]any{
			"dsn":   dsn,
			"sql":   fmt.Sprintf("SELECT id FROM `%s` ORDER BY id", table),
			"limit": 3,
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	rows := res.Output["rows"].Inline.([]map[string]any)
	if len(rows) != 3 {
		t.Errorf("rows = %d, want 3 (user limit)", len(rows))
	}
}

// TestMySQLQuery_BadSQL covers the db-error mapping in runQueryParsed (a
// query against a missing table) over a live connection.
func TestMySQLQuery_BadSQL(t *testing.T) {
	dsn, table := mysqlTestSetup(t)
	res, _ := executeMySQLQuery(t.Context(), core.Job{
		Params: map[string]any{
			"dsn": dsn,
			"sql": fmt.Sprintf("SELECT * FROM `%s_does_not_exist`", table),
		},
	}, nil)
	if res.Status != core.StatusError || res.Error == nil || res.Error.Code != "db" {
		t.Fatalf("status=%q err=%+v, want db error", res.Status, res.Error)
	}
}

// ----------------------------------------------------------------------
// Insert / upsert: live execBatch and create-table branches.
// ----------------------------------------------------------------------

// TestMySQLInsert_EmptyRowsCreatesTableOnly covers runInsert's
// create-table-then-zero-rows path: create_table true with headers but no
// rows returns inserted=0 and an empty table over a live connection.
func TestMySQLInsert_EmptyRowsCreatesTableOnly(t *testing.T) {
	dsn, table := mysqlTestSetup(t)
	res, err := executeMySQLInsertRows(t.Context(), core.Job{
		Params: map[string]any{
			"dsn":          dsn,
			"table":        table,
			"create_table": true,
			"column_types": map[string]any{"id": "INT"},
		},
		Input: map[string]core.Ref{
			// Empty rows: column order must ride on the rows Ref's Headers
			// (parseRowsInput derives from the rows otherwise, which is empty).
			"rows": {Inline: []map[string]any{}, Headers: []string{"id"}},
		},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if got := res.Output["inserted"].Inline; got != 0 {
		t.Errorf("inserted = %v, want 0", got)
	}
	if n := mysqlRowCount(t, dsn, table); n != 0 {
		t.Errorf("row count = %d, want 0", n)
	}
}

// TestMySQLInsert_CreateTableBadColumnType covers runInsert's
// create-table error mapping: an invalid column type makes the CREATE
// TABLE fail at the driver, returning a db error.
func TestMySQLInsert_CreateTableBadColumnType(t *testing.T) {
	dsn, table := mysqlTestSetup(t)
	res, _ := executeMySQLInsertRows(t.Context(), core.Job{
		Params: map[string]any{
			"dsn":          dsn,
			"table":        table,
			"create_table": true,
			"column_types": map[string]any{"id": "NOT_A_REAL_TYPE"},
		},
		Input: map[string]core.Ref{
			"rows":    {Inline: []map[string]any{{"id": 1}}},
			"headers": {Inline: []string{"id"}},
		},
	}, nil)
	if res.Status != core.StatusError || res.Error == nil || res.Error.Code != "db" {
		t.Fatalf("status=%q err=%+v, want db error", res.Status, res.Error)
	}
}

// TestMySQLUpsert_CreateTableWithUnique covers runUpsert's create-table
// branch (with the UNIQUE on conflict_columns) followed by a live upsert,
// when the table does not already exist.
func TestMySQLUpsert_CreateTableWithUnique(t *testing.T) {
	dsn, table := mysqlTestSetup(t)
	res, err := executeMySQLUpsertRows(t.Context(), core.Job{
		Params: map[string]any{
			"dsn":              dsn,
			"table":            table,
			"create_table":     true,
			"conflict_columns": []string{"id"},
			"column_types":     map[string]any{"id": "INT", "name": "VARCHAR(64)"},
		},
		Input: map[string]core.Ref{
			"rows": {Inline: []map[string]any{
				{"id": 1, "name": "Alice"},
				{"id": 2, "name": "Bob"},
			}},
			"headers": {Inline: []string{"id", "name"}},
		},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if got := res.Output["processed"].Inline; got != 2 {
		t.Errorf("processed = %v, want 2", got)
	}

	// Re-run with create_table true again (table now exists, IF NOT
	// EXISTS is a no-op) and an updated value, to land the update branch.
	res, _ = executeMySQLUpsertRows(t.Context(), core.Job{
		Params: map[string]any{
			"dsn":              dsn,
			"table":            table,
			"create_table":     true,
			"conflict_columns": []string{"id"},
			"column_types":     map[string]any{"id": "INT", "name": "VARCHAR(64)"},
		},
		Input: map[string]core.Ref{
			"rows":    {Inline: []map[string]any{{"id": 1, "name": "Alicia"}}},
			"headers": {Inline: []string{"id", "name"}},
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("second status=%q err=%+v", res.Status, res.Error)
	}
	if n := mysqlRowCount(t, dsn, table); n != 2 {
		t.Errorf("row count = %d, want 2 (upsert, no new row)", n)
	}
}

// TestMySQLUpsert_EmptyRowsCreatesTableOnly covers runUpsert's
// create-table-then-zero-rows early return (processed=0).
func TestMySQLUpsert_EmptyRowsCreatesTableOnly(t *testing.T) {
	dsn, table := mysqlTestSetup(t)
	res, _ := executeMySQLUpsertRows(t.Context(), core.Job{
		Params: map[string]any{
			"dsn":              dsn,
			"table":            table,
			"create_table":     true,
			"conflict_columns": []string{"id"},
			"column_types":     map[string]any{"id": "INT"},
		},
		Input: map[string]core.Ref{
			"rows": {Inline: []map[string]any{}, Headers: []string{"id"}},
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if got := res.Output["processed"].Inline; got != 0 {
		t.Errorf("processed = %v, want 0", got)
	}
}

// ----------------------------------------------------------------------
// execute* error-return branches (no live MySQL needed): the dsn
// bad-type returns and the sqlDB connect-error returns. A malformed DSN
// fails the registry's DSN parse, so the connect path returns a "db"
// error without ever reaching a server.
// ----------------------------------------------------------------------

func TestMySQLQuery_DSNWrongType(t *testing.T) {
	res, _ := executeMySQLQuery(t.Context(), core.Job{
		Params: map[string]any{"dsn": 123, "sql": "SELECT 1"},
	}, nil)
	if res.Status != core.StatusError || res.Error == nil || res.Error.Code != "bad_param" {
		t.Fatalf("status=%q err=%+v, want bad_param", res.Status, res.Error)
	}
}

func TestMySQLQuery_ConnectError(t *testing.T) {
	res, _ := executeMySQLQuery(t.Context(), core.Job{
		Params: map[string]any{"dsn": "not-a-valid-mysql-dsn", "sql": "SELECT 1"},
	}, nil)
	if res.Status != core.StatusError || res.Error == nil || res.Error.Code != "db" {
		t.Fatalf("status=%q err=%+v, want db", res.Status, res.Error)
	}
}

func TestMySQLInsert_DSNWrongType(t *testing.T) {
	res, _ := executeMySQLInsertRows(t.Context(), core.Job{
		Params: map[string]any{"dsn": 123, "table": "t"},
		Input:  map[string]core.Ref{"rows": {Inline: []map[string]any{{"a": "1"}}}},
	}, nil)
	if res.Status != core.StatusError || res.Error == nil || res.Error.Code != "bad_param" {
		t.Fatalf("status=%q err=%+v, want bad_param", res.Status, res.Error)
	}
}

func TestMySQLInsert_TableWrongType(t *testing.T) {
	res, _ := executeMySQLInsertRows(t.Context(), core.Job{
		Params: map[string]any{"dsn": "x", "table": 123},
		Input:  map[string]core.Ref{"rows": {Inline: []map[string]any{{"a": "1"}}}},
	}, nil)
	if res.Status != core.StatusError || res.Error == nil || res.Error.Code != "bad_param" {
		t.Fatalf("status=%q err=%+v, want bad_param", res.Status, res.Error)
	}
}

func TestMySQLInsert_ConnectError(t *testing.T) {
	res, _ := executeMySQLInsertRows(t.Context(), core.Job{
		Params: map[string]any{"dsn": "not-a-valid-mysql-dsn", "table": "t"},
		Input:  map[string]core.Ref{"rows": {Inline: []map[string]any{{"a": "1"}}}},
	}, nil)
	if res.Status != core.StatusError || res.Error == nil || res.Error.Code != "db" {
		t.Fatalf("status=%q err=%+v, want db", res.Status, res.Error)
	}
}

func TestMySQLUpsert_DSNWrongType(t *testing.T) {
	res, _ := executeMySQLUpsertRows(t.Context(), core.Job{
		Params: map[string]any{"dsn": 123, "table": "t", "conflict_columns": []string{"id"}},
		Input:  map[string]core.Ref{"rows": {Inline: []map[string]any{{"id": "1"}}}},
	}, nil)
	if res.Status != core.StatusError || res.Error == nil || res.Error.Code != "bad_param" {
		t.Fatalf("status=%q err=%+v, want bad_param", res.Status, res.Error)
	}
}

func TestMySQLUpsert_TableWrongType(t *testing.T) {
	res, _ := executeMySQLUpsertRows(t.Context(), core.Job{
		Params: map[string]any{"dsn": "x", "table": 123, "conflict_columns": []string{"id"}},
		Input:  map[string]core.Ref{"rows": {Inline: []map[string]any{{"id": "1"}}}},
	}, nil)
	if res.Status != core.StatusError || res.Error == nil || res.Error.Code != "bad_param" {
		t.Fatalf("status=%q err=%+v, want bad_param", res.Status, res.Error)
	}
}

func TestMySQLUpsert_UnsafeTableName(t *testing.T) {
	res, _ := executeMySQLUpsertRows(t.Context(), core.Job{
		Params: map[string]any{"dsn": "x", "table": "ta\x00ble", "conflict_columns": []string{"id"}},
		Input:  map[string]core.Ref{"rows": {Inline: []map[string]any{{"id": "1"}}}},
	}, nil)
	if res.Status != core.StatusError || res.Error == nil || res.Error.Code != "bad_param" {
		t.Fatalf("status=%q err=%+v, want bad_param", res.Status, res.Error)
	}
}

func TestMySQLUpsert_BadRowsInput(t *testing.T) {
	// A rows Inline that normalizeRows can't handle drives the
	// parseRowsInput error return.
	res, _ := executeMySQLUpsertRows(t.Context(), core.Job{
		Params: map[string]any{"dsn": "x", "table": "t", "conflict_columns": []string{"id"}},
		Input:  map[string]core.Ref{"rows": {Inline: 12345}},
	}, nil)
	if res.Status != core.StatusError || res.Error == nil {
		t.Fatalf("status=%q err=%+v, want an error", res.Status, res.Error)
	}
}

func TestMySQLUpsert_ConnectError(t *testing.T) {
	res, _ := executeMySQLUpsertRows(t.Context(), core.Job{
		Params: map[string]any{
			"dsn": "not-a-valid-mysql-dsn", "table": "t",
			"conflict_columns": []string{"id"},
		},
		Input: map[string]core.Ref{
			"rows": {Inline: []map[string]any{{"id": "1"}}, Headers: []string{"id"}},
		},
	}, nil)
	if res.Status != core.StatusError || res.Error == nil || res.Error.Code != "db" {
		t.Fatalf("status=%q err=%+v, want db", res.Status, res.Error)
	}
}

// ----------------------------------------------------------------------
// verify: live success path for verifyMySQL.
// ----------------------------------------------------------------------

// TestVerifyMySQL_Live covers verifyMySQL's success return (verify.go:91)
// against the live test server.
func TestVerifyMySQL_Live(t *testing.T) {
	dsn, _ := mysqlTestSetup(t)
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	if err := verifyMySQL(ctx, map[string]string{"dsn": dsn}); err != nil {
		t.Fatalf("verifyMySQL(live) = %v, want nil", err)
	}
}
