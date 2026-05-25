package db

import (
	"context"
	"fmt"
	"testing"
	"time"

	"git.sr.ht/~klahr/hazy-flow/core"
	"github.com/jackc/pgx/v5"
)

// seedQueryTable creates a small table with mixed-type columns and
// inserts the given rows. Used by the integration tests below to give
// each one isolated, predictable data.
func seedQueryTable(t *testing.T, dsn, table string, rows [][]any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)
	stmt := fmt.Sprintf(`CREATE TABLE "public"."%s" (
		id integer,
		name text,
		score numeric,
		active boolean
	)`, table)
	if _, err := conn.Exec(ctx, stmt); err != nil {
		t.Fatalf("create: %v", err)
	}
	insert := fmt.Sprintf(`INSERT INTO "public"."%s" (id, name, score, active) VALUES ($1, $2, $3, $4)`, table)
	for i, r := range rows {
		if _, err := conn.Exec(ctx, insert, r...); err != nil {
			t.Fatalf("insert row %d: %v", i, err)
		}
	}
}

func TestPostgresQuery_NoParams(t *testing.T) {
	dsn, table := pgTestSetup(t)
	seedQueryTable(t, dsn, table, [][]any{
		{1, "Alice", 9.5, true},
		{2, "Bob", 7.0, false},
	})
	res, err := executePostgresQuery(t.Context(), core.Job{
		Params: map[string]any{
			"dsn": dsn,
			"sql": fmt.Sprintf(`SELECT id, name FROM "public"."%s" ORDER BY id`, table),
		},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	cols := res.Output["columns"].Inline.([]string)
	if got, want := cols, []string{"id", "name"}; len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("columns = %v, want %v", got, want)
	}
	rows := res.Output["rows"].Inline.([]map[string]any)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	if rows[0]["name"] != "Alice" || rows[1]["name"] != "Bob" {
		t.Errorf("rows = %+v", rows)
	}
}

func TestPostgresQuery_PositionalParams(t *testing.T) {
	dsn, table := pgTestSetup(t)
	seedQueryTable(t, dsn, table, [][]any{
		{1, "Alice", 9.5, true},
		{2, "Bob", 7.0, false},
		{3, "Carol", 8.5, true},
	})
	res, _ := executePostgresQuery(t.Context(), core.Job{
		Params: map[string]any{
			"dsn":    dsn,
			"sql":    fmt.Sprintf(`SELECT name FROM "public"."%s" WHERE active = $1 ORDER BY id`, table),
			"params": []any{true},
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	rows := res.Output["rows"].Inline.([]map[string]any)
	if len(rows) != 2 || rows[0]["name"] != "Alice" || rows[1]["name"] != "Carol" {
		t.Errorf("rows = %+v, want Alice, Carol", rows)
	}
}

func TestPostgresQuery_TypedValuesPreserved(t *testing.T) {
	// pgx returns Go-typed values per the column's pg type — verifying
	// we don't string-stringify everything on the way out (excel_read
	// does, postgres_query intentionally doesn't).
	dsn, table := pgTestSetup(t)
	seedQueryTable(t, dsn, table, [][]any{
		{42, "x", 3.14, true},
	})
	res, _ := executePostgresQuery(t.Context(), core.Job{
		Params: map[string]any{
			"dsn": dsn,
			"sql": fmt.Sprintf(`SELECT id, score, active FROM "public"."%s"`, table),
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	row := res.Output["rows"].Inline.([]map[string]any)[0]
	if _, ok := row["id"].(int32); !ok {
		// pgx returns int4 as int32; either int32 or int64 is acceptable.
		if _, ok := row["id"].(int64); !ok {
			t.Errorf("id = %T %v, want integer", row["id"], row["id"])
		}
	}
	if _, ok := row["active"].(bool); !ok {
		t.Errorf("active = %T %v, want bool", row["active"], row["active"])
	}
}

func TestPostgresQuery_LimitCaps(t *testing.T) {
	dsn, table := pgTestSetup(t)
	seedQueryTable(t, dsn, table, [][]any{
		{1, "a", 0.0, true},
		{2, "b", 0.0, true},
		{3, "c", 0.0, true},
		{4, "d", 0.0, true},
	})
	res, _ := executePostgresQuery(t.Context(), core.Job{
		Params: map[string]any{
			"dsn":   dsn,
			"sql":   fmt.Sprintf(`SELECT id FROM "public"."%s" ORDER BY id`, table),
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

func TestPostgresQuery_NoRows(t *testing.T) {
	dsn, table := pgTestSetup(t)
	seedQueryTable(t, dsn, table, nil)
	res, _ := executePostgresQuery(t.Context(), core.Job{
		Params: map[string]any{
			"dsn": dsn,
			"sql": fmt.Sprintf(`SELECT id, name FROM "public"."%s"`, table),
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	rows := res.Output["rows"].Inline.([]map[string]any)
	if len(rows) != 0 {
		t.Errorf("rows = %d, want 0", len(rows))
	}
	cols := res.Output["columns"].Inline.([]string)
	if len(cols) != 2 {
		t.Errorf("columns still emitted from schema: got %v", cols)
	}
}

func TestPostgresQuery_BadSQL(t *testing.T) {
	dsn, _ := pgTestSetup(t)
	res, _ := executePostgresQuery(t.Context(), core.Job{
		Params: map[string]any{
			"dsn": dsn,
			"sql": "SELECT nope FROM not_a_real_table_zzz",
		},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "db" {
		t.Errorf("status=%q code=%q, want error/db", res.Status, res.Error.Code)
	}
}

// ----------------------------------------------------------------------
// Unit tests — no Postgres required.
// ----------------------------------------------------------------------

func TestPostgresQuery_MissingDSN(t *testing.T) {
	res, _ := executePostgresQuery(t.Context(), core.Job{
		Params: map[string]any{"sql": "SELECT 1"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%q, want bad_param", res.Status, res.Error.Code)
	}
}

func TestPostgresQuery_MissingSQL(t *testing.T) {
	res, _ := executePostgresQuery(t.Context(), core.Job{
		Params: map[string]any{"dsn": "postgres://"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%q, want bad_param", res.Status, res.Error.Code)
	}
}

func TestPostgresQuery_EmptySQL(t *testing.T) {
	res, _ := executePostgresQuery(t.Context(), core.Job{
		Params: map[string]any{"dsn": "postgres://", "sql": ""},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%q, want bad_param", res.Status, res.Error.Code)
	}
}

func TestPostgresQuery_ParamsWrongType(t *testing.T) {
	// 'params' must be an array; a scalar should be rejected before
	// we open a connection.
	res, _ := executePostgresQuery(t.Context(), core.Job{
		Params: map[string]any{
			"dsn":    "postgres://",
			"sql":    "SELECT 1",
			"params": "not-an-array",
		},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%q, want bad_param", res.Status, res.Error.Code)
	}
}

func TestPostgresQuery_NegativeLimit(t *testing.T) {
	res, _ := executePostgresQuery(t.Context(), core.Job{
		Params: map[string]any{
			"dsn":   "postgres://",
			"sql":   "SELECT 1",
			"limit": -3,
		},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%q, want bad_param", res.Status, res.Error.Code)
	}
}
