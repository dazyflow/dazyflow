// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package db

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
	"github.com/jackc/pgx/v5"
)

// Integration tests for postgres_insert_rows. Skipped unless
// DAZYFLOW_TEST_DB is set, matching the project convention:
//
//   DAZYFLOW_TEST_DB=postgres://localhost/dazyflow_test \
//     go test ./drops/db/
//
// The unit tests below (validation, param parsing) run unconditionally
// — they exercise the code paths that don't actually open a connection.

// pgTestSetup picks a unique table name per test (suffixed with a
// timestamp) and registers cleanup that drops it. Returns the DSN and
// the table name; t.Skip is called when the env var isn't set so
// individual tests don't need to repeat the gate.
func pgTestSetup(t *testing.T) (dsn, table string) {
	t.Helper()
	dsn = os.Getenv("DAZYFLOW_TEST_DB")
	if dsn == "" {
		t.Skip("set DAZYFLOW_TEST_DB to run Postgres integration tests")
	}
	// Unique per test: t.Name with non-ident chars stripped + ns suffix.
	base := strip(t.Name())
	table = fmt.Sprintf("dz_test_%s_%d", base, time.Now().UnixNano())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		conn, err := pgx.Connect(ctx, dsn)
		if err != nil {
			t.Logf("cleanup connect: %v", err)
			return
		}
		defer conn.Close(ctx)
		_, _ = conn.Exec(ctx, fmt.Sprintf(`DROP TABLE IF EXISTS "public"."%s"`, table))
	})
	return dsn, table
}

func strip(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			out = append(out, c)
		}
	}
	if len(out) > 32 {
		out = out[:32]
	}
	return string(out)
}

func pgRowCount(t *testing.T, dsn, table string) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)
	var n int
	if err := conn.QueryRow(ctx, fmt.Sprintf(`SELECT count(*) FROM "public"."%s"`, table)).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

func TestPostgresInsert_CreateAndInsert(t *testing.T) {
	dsn, table := pgTestSetup(t)
	res, err := executePostgresInsertRows(t.Context(), core.Job{
		Params: map[string]any{
			"dsn":          dsn,
			"table":        table,
			"create_table": true,
			"column_types": map[string]any{"age": "integer"},
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
	if got := res.Output["inserted"].Inline; got != 2 {
		t.Errorf("inserted = %v, want 2", got)
	}
	if n := pgRowCount(t, dsn, table); n != 2 {
		t.Errorf("row count = %d, want 2", n)
	}
}

func TestPostgresInsert_RollbackOnFailure(t *testing.T) {
	dsn, table := pgTestSetup(t)

	// Seed a table with a single column "a"; then try to insert a
	// row that references column "b" too — should fail and roll back
	// the whole batch.
	ctx := t.Context()
	conn, _ := pgx.Connect(ctx, dsn)
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, fmt.Sprintf(`CREATE TABLE "public"."%s" (a text)`, table)); err != nil {
		t.Fatalf("seed: %v", err)
	}

	res, _ := executePostgresInsertRows(ctx, core.Job{
		Params: map[string]any{"dsn": dsn, "table": table},
		Input: map[string]core.Ref{
			"rows":    {Inline: []map[string]any{{"a": "ok"}, {"b": "bad"}}},
			"headers": {Inline: []string{"a", "b"}},
		},
	}, nil)
	if res.Status != core.StatusError {
		t.Fatalf("status=%q, want error", res.Status)
	}
	if n := pgRowCount(t, dsn, table); n != 0 {
		t.Errorf("rollback failed: rows = %d, want 0", n)
	}
}

func TestPostgresInsert_NamedSchema(t *testing.T) {
	dsn, table := pgTestSetup(t)
	// Use a custom schema; assume the test DB allows CREATE SCHEMA.
	schemaName := fmt.Sprintf("dz_test_%d", time.Now().UnixNano())
	ctx := t.Context()
	conn, _ := pgx.Connect(ctx, dsn)
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, fmt.Sprintf(`CREATE SCHEMA "%s"`, schemaName)); err != nil {
		t.Skipf("can't create schema (DB user may lack permission): %v", err)
	}
	t.Cleanup(func() {
		c, err := pgx.Connect(context.Background(), dsn)
		if err == nil {
			defer c.Close(context.Background())
			_, _ = c.Exec(context.Background(), fmt.Sprintf(`DROP SCHEMA "%s" CASCADE`, schemaName))
		}
	})

	res, _ := executePostgresInsertRows(ctx, core.Job{
		Params: map[string]any{
			"dsn":          dsn,
			"schema":       schemaName,
			"table":        table,
			"create_table": true,
		},
		Input: map[string]core.Ref{
			"rows":    {Inline: []map[string]any{{"x": "1"}}},
			"headers": {Inline: []string{"x"}},
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	var n int
	if err := conn.QueryRow(ctx, fmt.Sprintf(`SELECT count(*) FROM "%s"."%s"`, schemaName, table)).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("rows in custom schema = %d, want 1", n)
	}
}

// ----------------------------------------------------------------------
// Unit tests — no Postgres needed. Cover the validation surface that
// the drop performs before opening any connection.
// ----------------------------------------------------------------------

// Only the genuinely-unsafe shapes are pre-rejected. Names like
// "with space" or `"public; DROP"` are valid Postgres identifiers
// when double-quoted — they used to trip the [A-Za-z0-9_] regex but
// don't anymore; the drop quotes them and Postgres handles them.
func TestPostgresInsert_RejectsUnsafeTableName(t *testing.T) {
	for _, name := range []string{"", "tab\x00le"} {
		t.Run(name, func(t *testing.T) {
			res, _ := executePostgresInsertRows(t.Context(), core.Job{
				Params: map[string]any{"dsn": "postgres://", "table": name},
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

func TestPostgresInsert_RejectsUnsafeSchemaName(t *testing.T) {
	res, _ := executePostgresInsertRows(t.Context(), core.Job{
		Params: map[string]any{
			"dsn":    "postgres://",
			"table":  "ok",
			"schema": "sch\x00ema",
		},
		Input: map[string]core.Ref{
			"rows": {Inline: []map[string]any{{"a": "1"}}},
		},
	}, nil)
	if res.Status != core.StatusError || res.Error == nil || res.Error.Code != "bad_param" {
		t.Errorf("status=%q error=%+v, want bad_param", res.Status, res.Error)
	}
}

func TestPostgresInsert_RejectsUnsafeColumnName(t *testing.T) {
	res, _ := executePostgresInsertRows(t.Context(), core.Job{
		Params: map[string]any{"dsn": "postgres://", "table": "ok"},
		Input: map[string]core.Ref{
			"rows":    {Inline: []map[string]any{{"co\x00l": "x"}}},
			"headers": {Inline: []string{"co\x00l"}},
		},
	}, nil)
	if res.Status != core.StatusError || res.Error == nil || res.Error.Code != "bad_input" {
		t.Errorf("status=%q error=%+v, want bad_input", res.Status, res.Error)
	}
}

func TestPostgresInsert_MissingDSN(t *testing.T) {
	res, _ := executePostgresInsertRows(t.Context(), core.Job{
		Params: map[string]any{"table": "t"},
		Input: map[string]core.Ref{
			"rows": {Inline: []map[string]any{{"a": "1"}}},
		},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%q, want bad_param", res.Status, res.Error.Code)
	}
}

func TestPostgresInsert_MissingRows(t *testing.T) {
	res, _ := executePostgresInsertRows(t.Context(), core.Job{
		Params: map[string]any{"dsn": "postgres://", "table": "t"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "missing_input" {
		t.Errorf("status=%q code=%q, want missing_input", res.Status, res.Error.Code)
	}
}
