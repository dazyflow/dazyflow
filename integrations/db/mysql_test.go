package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"git.sr.ht/~klahr/hazy-flow/core"
	_ "github.com/go-sql-driver/mysql"
)

// Integration tests for the mysql_* drops. Skipped unless
// HAZYFLOW_TEST_MYSQL is set:
//
//   HAZYFLOW_TEST_MYSQL='user:pass@tcp(localhost:3306)/hazyflow_test?parseTime=true' \
//     go test ./integrations/db/
//
// Use a dedicated test database. We create + drop tables named
// hz_test_*_<unix-ns> per test, so concurrent runs in the same DB
// don't collide.

func mysqlTestSetup(t *testing.T) (dsn, table string) {
	t.Helper()
	dsn = os.Getenv("HAZYFLOW_TEST_MYSQL")
	if dsn == "" {
		t.Skip("set HAZYFLOW_TEST_MYSQL to run MySQL integration tests")
	}
	base := strip(t.Name())
	table = fmt.Sprintf("hz_test_%s_%d", base, time.Now().UnixNano())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		db, err := sql.Open("mysql", dsn)
		if err != nil {
			t.Logf("cleanup open: %v", err)
			return
		}
		defer db.Close()
		_, _ = db.ExecContext(ctx, fmt.Sprintf("DROP TABLE IF EXISTS `%s`", table))
	})
	return dsn, table
}

func mysqlRowCount(t *testing.T, dsn, table string) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRowContext(ctx, fmt.Sprintf("SELECT count(*) FROM `%s`", table)).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

func TestMySQLInsert_CreateAndInsert(t *testing.T) {
	dsn, table := mysqlTestSetup(t)
	res, err := executeMySQLInsertRows(t.Context(), core.Job{
		Params: map[string]any{
			"dsn":          dsn,
			"table":        table,
			"create_table": true,
			"column_types": map[string]any{"id": "INT", "name": "VARCHAR(64)"},
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
	if got := res.Output["inserted"].Inline; got != 2 {
		t.Errorf("inserted = %v, want 2", got)
	}
	if n := mysqlRowCount(t, dsn, table); n != 2 {
		t.Errorf("row count = %d, want 2", n)
	}
}

func TestMySQLInsert_RollbackOnFailure(t *testing.T) {
	dsn, table := mysqlTestSetup(t)
	ctx := t.Context()
	db, _ := sql.Open("mysql", dsn)
	defer db.Close()
	if _, err := db.ExecContext(ctx, fmt.Sprintf("CREATE TABLE `%s` (a VARCHAR(8))", table)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	res, _ := executeMySQLInsertRows(ctx, core.Job{
		Params: map[string]any{"dsn": dsn, "table": table},
		Input: map[string]core.Ref{
			"rows":    {Inline: []map[string]any{{"a": "ok"}, {"b": "bad"}}},
			"headers": {Inline: []string{"a", "b"}},
		},
	}, nil)
	if res.Status != core.StatusError {
		t.Fatalf("status=%q, want error", res.Status)
	}
	if n := mysqlRowCount(t, dsn, table); n != 0 {
		t.Errorf("rollback failed: rows = %d, want 0", n)
	}
}

func TestMySQLQuery_NoParams(t *testing.T) {
	dsn, table := mysqlTestSetup(t)
	ctx := t.Context()
	db, _ := sql.Open("mysql", dsn)
	defer db.Close()
	if _, err := db.ExecContext(ctx, fmt.Sprintf(
		"CREATE TABLE `%s` (id INT, name VARCHAR(64))", table)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	for i, name := range []string{"Alice", "Bob"} {
		if _, err := db.ExecContext(ctx, fmt.Sprintf(
			"INSERT INTO `%s` VALUES (?, ?)", table), i+1, name); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	res, _ := executeMySQLQuery(ctx, core.Job{
		Params: map[string]any{
			"dsn": dsn,
			"sql": fmt.Sprintf("SELECT id, name FROM `%s` ORDER BY id", table),
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	rows := res.Output["rows"].Inline.([]map[string]any)
	if len(rows) != 2 || rows[0]["name"] != "Alice" {
		t.Errorf("rows = %+v", rows)
	}
}

func TestMySQLQuery_PositionalParams(t *testing.T) {
	dsn, table := mysqlTestSetup(t)
	ctx := t.Context()
	db, _ := sql.Open("mysql", dsn)
	defer db.Close()
	_, _ = db.ExecContext(ctx, fmt.Sprintf("CREATE TABLE `%s` (id INT, active TINYINT)", table))
	_, _ = db.ExecContext(ctx, fmt.Sprintf("INSERT INTO `%s` VALUES (1, 1), (2, 0), (3, 1)", table))
	res, _ := executeMySQLQuery(ctx, core.Job{
		Params: map[string]any{
			"dsn":    dsn,
			"sql":    fmt.Sprintf("SELECT id FROM `%s` WHERE active = ? ORDER BY id", table),
			"params": []any{1},
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	rows := res.Output["rows"].Inline.([]map[string]any)
	if len(rows) != 2 {
		t.Errorf("rows = %d, want 2", len(rows))
	}
}

func TestMySQLUpsert_InsertAndUpdate(t *testing.T) {
	dsn, table := mysqlTestSetup(t)
	ctx := t.Context()
	db, _ := sql.Open("mysql", dsn)
	defer db.Close()
	if _, err := db.ExecContext(ctx, fmt.Sprintf(
		"CREATE TABLE `%s` (id INT PRIMARY KEY, name VARCHAR(64))", table)); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// First pass: pure inserts.
	res, _ := executeMySQLUpsertRows(ctx, core.Job{
		Params: map[string]any{
			"dsn":              dsn,
			"table":            table,
			"conflict_columns": []string{"id"},
		},
		Input: map[string]core.Ref{
			"rows": {Inline: []map[string]any{
				{"id": 1, "name": "Alice"},
				{"id": 2, "name": "Bob"},
			}},
			"headers": {Inline: []string{"id", "name"}},
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("first: status=%q err=%+v", res.Status, res.Error)
	}

	// Second pass: re-insert with new values + one new id.
	res, _ = executeMySQLUpsertRows(ctx, core.Job{
		Params: map[string]any{
			"dsn":              dsn,
			"table":            table,
			"conflict_columns": []string{"id"},
		},
		Input: map[string]core.Ref{
			"rows": {Inline: []map[string]any{
				{"id": 1, "name": "Alicia"},
				{"id": 2, "name": "Bobby"},
				{"id": 3, "name": "Carol"},
			}},
			"headers": {Inline: []string{"id", "name"}},
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("second: status=%q err=%+v", res.Status, res.Error)
	}
	if got := res.Output["processed"].Inline; got != 3 {
		t.Errorf("processed = %v, want 3", got)
	}
	// Verify the updates landed.
	var name string
	if err := db.QueryRowContext(ctx, fmt.Sprintf(
		"SELECT name FROM `%s` WHERE id = 1", table)).Scan(&name); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if name != "Alicia" {
		t.Errorf("name = %q, want Alicia (update should apply)", name)
	}
}

func TestMySQLUpsert_PartialUpdate(t *testing.T) {
	dsn, table := mysqlTestSetup(t)
	ctx := t.Context()
	db, _ := sql.Open("mysql", dsn)
	defer db.Close()
	_, _ = db.ExecContext(ctx, fmt.Sprintf(
		"CREATE TABLE `%s` (id INT PRIMARY KEY, name VARCHAR(64), score INT)", table))

	_, _ = executeMySQLUpsertRows(ctx, core.Job{
		Params: map[string]any{
			"dsn": dsn, "table": table, "conflict_columns": []string{"id"},
		},
		Input: map[string]core.Ref{
			"rows":    {Inline: []map[string]any{{"id": 1, "name": "orig", "score": 5}}},
			"headers": {Inline: []string{"id", "name", "score"}},
		},
	}, nil)

	// Only update name; score should stay at 5.
	res, _ := executeMySQLUpsertRows(ctx, core.Job{
		Params: map[string]any{
			"dsn": dsn, "table": table,
			"conflict_columns": []string{"id"},
			"update_columns":   []string{"name"},
		},
		Input: map[string]core.Ref{
			"rows":    {Inline: []map[string]any{{"id": 1, "name": "updated", "score": 999}}},
			"headers": {Inline: []string{"id", "name", "score"}},
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	var name string
	var score int
	if err := db.QueryRowContext(ctx, fmt.Sprintf(
		"SELECT name, score FROM `%s` WHERE id = 1", table)).Scan(&name, &score); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if name != "updated" {
		t.Errorf("name = %q, want updated", name)
	}
	if score != 5 {
		t.Errorf("score = %d, want preserved at 5", score)
	}
}

func TestMySQLUpsert_DoNothingEquivalent(t *testing.T) {
	// update_columns=[] should set the conflict column to itself —
	// a no-op write that leaves the existing row untouched.
	dsn, table := mysqlTestSetup(t)
	ctx := t.Context()
	db, _ := sql.Open("mysql", dsn)
	defer db.Close()
	_, _ = db.ExecContext(ctx, fmt.Sprintf(
		"CREATE TABLE `%s` (id INT PRIMARY KEY, name VARCHAR(64))", table))

	_, _ = executeMySQLUpsertRows(ctx, core.Job{
		Params: map[string]any{
			"dsn": dsn, "table": table, "conflict_columns": []string{"id"},
		},
		Input: map[string]core.Ref{
			"rows":    {Inline: []map[string]any{{"id": 1, "name": "original"}}},
			"headers": {Inline: []string{"id", "name"}},
		},
	}, nil)

	res, _ := executeMySQLUpsertRows(ctx, core.Job{
		Params: map[string]any{
			"dsn": dsn, "table": table,
			"conflict_columns": []string{"id"},
			"update_columns":   []string{},
		},
		Input: map[string]core.Ref{
			"rows":    {Inline: []map[string]any{{"id": 1, "name": "should-be-ignored"}}},
			"headers": {Inline: []string{"id", "name"}},
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	var name string
	if err := db.QueryRowContext(ctx, fmt.Sprintf(
		"SELECT name FROM `%s` WHERE id = 1", table)).Scan(&name); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if name != "original" {
		t.Errorf("name = %q, want preserved at 'original'", name)
	}
}

// ----------------------------------------------------------------------
// Unit tests — no MySQL required.
// ----------------------------------------------------------------------

// Only the genuinely-unsafe shapes are pre-rejected now. Names like
// "with space" or "with-dash" are valid MySQL identifiers when
// backtick-quoted, so they go through to the driver — which lands
// at the connect stage in unit tests without a real MySQL server.
func TestMySQLInsert_RejectsUnsafeTableName(t *testing.T) {
	for _, name := range []string{"", "tab\x00le"} {
		t.Run(name, func(t *testing.T) {
			res, _ := executeMySQLInsertRows(t.Context(), core.Job{
				Params: map[string]any{"dsn": "user:pw@tcp(localhost:3306)/db", "table": name},
				Input: map[string]core.Ref{
					"rows": {Inline: []map[string]any{{"a": "1"}}},
				},
			}, nil)
			if res.Status != core.StatusError || res.Error == nil || res.Error.Code != "bad_param" {
				t.Errorf("status=%q error=%+v, want bad_param", res.Status, res.Error)
			}
		})
	}
}

func TestMySQLInsert_RejectsUnsafeColumnName(t *testing.T) {
	res, _ := executeMySQLInsertRows(t.Context(), core.Job{
		Params: map[string]any{"dsn": "x", "table": "t"},
		Input: map[string]core.Ref{
			"rows":    {Inline: []map[string]any{{"co\x00l": "x"}}},
			"headers": {Inline: []string{"co\x00l"}},
		},
	}, nil)
	if res.Status != core.StatusError || res.Error == nil || res.Error.Code != "bad_input" {
		t.Errorf("status=%q error=%+v, want bad_input", res.Status, res.Error)
	}
}

func TestMySQLInsert_MissingDSN(t *testing.T) {
	res, _ := executeMySQLInsertRows(t.Context(), core.Job{
		Params: map[string]any{"table": "t"},
		Input:  map[string]core.Ref{"rows": {Inline: []map[string]any{{"a": "1"}}}},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%q, want bad_param", res.Status, res.Error.Code)
	}
}

func TestMySQLQuery_MissingSQL(t *testing.T) {
	res, _ := executeMySQLQuery(t.Context(), core.Job{
		Params: map[string]any{"dsn": "x"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%q, want bad_param", res.Status, res.Error.Code)
	}
}

func TestMySQLQuery_ParamsWrongType(t *testing.T) {
	res, _ := executeMySQLQuery(t.Context(), core.Job{
		Params: map[string]any{
			"dsn": "x", "sql": "SELECT 1",
			"params": "not-an-array",
		},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%q, want bad_param", res.Status, res.Error.Code)
	}
}

func TestMySQLUpsert_MissingConflictColumns(t *testing.T) {
	res, _ := executeMySQLUpsertRows(t.Context(), core.Job{
		Params: map[string]any{"dsn": "x", "table": "t"},
		Input:  map[string]core.Ref{"rows": {Inline: []map[string]any{{"a": "1"}}}},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%q, want bad_param", res.Status, res.Error.Code)
	}
}

func TestMySQLUpsert_ConflictColumnNotInHeaders(t *testing.T) {
	res, _ := executeMySQLUpsertRows(t.Context(), core.Job{
		Params: map[string]any{
			"dsn": "x", "table": "t",
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

func TestMySQLUpsert_UnsafeConflictColumn(t *testing.T) {
	res, _ := executeMySQLUpsertRows(t.Context(), core.Job{
		Params: map[string]any{
			"dsn": "x", "table": "t",
			"conflict_columns": []string{"id; DROP"},
		},
		Input: map[string]core.Ref{
			"rows": {Inline: []map[string]any{{"a": "1"}}},
		},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%q, want bad_param", res.Status, res.Error.Code)
	}
}

// ----------------------------------------------------------------------
// SQL DB registry tests (parallel to TestPGRegistry_*).
// ----------------------------------------------------------------------

func TestSQLDBRegistry_BadDSNDoesNotPoison(t *testing.T) {
	r := newSQLDBRegistry("mysql", time.Hour, time.Hour)
	_, err := r.sqlDB(t.Context(), "acme", "totally-not-a-valid-mysql-dsn")
	if err == nil {
		t.Fatal("expected error from malformed DSN")
	}
	if len(r.dbs) != 0 {
		t.Errorf("bad DSN poisoned registry: %d entries", len(r.dbs))
	}
}

func TestSQLDBRegistry_SweepEvictsIdle(t *testing.T) {
	r := newSQLDBRegistry("mysql", 100*time.Millisecond, 0)
	now := time.Now()
	r.dbs[pgPoolKey{"t", "fresh"}] = &sqlDBEntry{db: nil, lastUse: now}
	r.dbs[pgPoolKey{"t", "stale"}] = &sqlDBEntry{db: nil, lastUse: now.Add(-200 * time.Millisecond)}

	r.mu.Lock()
	r.sweepLocked(now)
	r.mu.Unlock()

	if _, ok := r.dbs[pgPoolKey{"t", "fresh"}]; !ok {
		t.Error("fresh entry evicted")
	}
	if _, ok := r.dbs[pgPoolKey{"t", "stale"}]; ok {
		t.Error("stale entry not evicted")
	}
}
