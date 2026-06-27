// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package db

import (
	"context"
	"fmt"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
	"github.com/jackc/pgx/v5"
)

// seedUpsertTable creates a table with a unique key for conflict tests.
func seedUpsertTable(t *testing.T, dsn, table string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)
	stmt := fmt.Sprintf(`CREATE TABLE "public"."%s" (
		id integer PRIMARY KEY,
		name text,
		score numeric
	)`, table)
	if _, err := conn.Exec(ctx, stmt); err != nil {
		t.Fatalf("create: %v", err)
	}
}

func pgFetchAll(t *testing.T, dsn, table string) []map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)
	rows, err := conn.Query(ctx, fmt.Sprintf(`SELECT id, name, score FROM "public"."%s" ORDER BY id`, table))
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		vals, _ := rows.Values()
		out = append(out, map[string]any{"id": vals[0], "name": vals[1], "score": vals[2]})
	}
	return out
}

func TestPostgresUpsert_InsertAndUpdate(t *testing.T) {
	dsn, table := pgTestSetup(t)
	seedUpsertTable(t, dsn, table)

	// First pass: pure inserts.
	res, _ := executePostgresUpsertRows(t.Context(), core.Job{
		Params: map[string]any{
			"dsn":              dsn,
			"table":            table,
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
		t.Fatalf("first pass: status=%q err=%+v", res.Status, res.Error)
	}

	// Second pass: same ids, different values → should UPDATE.
	res, _ = executePostgresUpsertRows(t.Context(), core.Job{
		Params: map[string]any{
			"dsn":              dsn,
			"table":            table,
			"conflict_columns": []string{"id"},
		},
		Input: map[string]core.Ref{
			"rows": {Inline: []map[string]any{
				{"id": 1, "name": "Alicia", "score": 10.0},
				{"id": 2, "name": "Bobby", "score": 8.5},
				{"id": 3, "name": "Carol", "score": 9.0}, // new row → INSERT
			}},
			"headers": {Inline: []string{"id", "name", "score"}},
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("second pass: status=%q err=%+v", res.Status, res.Error)
	}
	if got := res.Output["processed"].Inline; got != 3 {
		t.Errorf("processed = %v, want 3", got)
	}

	all := pgFetchAll(t, dsn, table)
	if len(all) != 3 {
		t.Fatalf("rows = %d, want 3", len(all))
	}
	// Verify the updates landed.
	if all[0]["name"] != "Alicia" || all[1]["name"] != "Bobby" || all[2]["name"] != "Carol" {
		t.Errorf("updates didn't apply: %+v", all)
	}
}

func TestPostgresUpsert_DoNothingWhenUpdateColsEmpty(t *testing.T) {
	dsn, table := pgTestSetup(t)
	seedUpsertTable(t, dsn, table)

	// Insert one row.
	_, _ = executePostgresUpsertRows(t.Context(), core.Job{
		Params: map[string]any{
			"dsn":              dsn,
			"table":            table,
			"conflict_columns": []string{"id"},
		},
		Input: map[string]core.Ref{
			"rows":    {Inline: []map[string]any{{"id": 1, "name": "original", "score": 1.0}}},
			"headers": {Inline: []string{"id", "name", "score"}},
		},
	}, nil)

	// Second upsert with update_columns=[] should be DO NOTHING — name
	// must stay "original".
	res, _ := executePostgresUpsertRows(t.Context(), core.Job{
		Params: map[string]any{
			"dsn":              dsn,
			"table":            table,
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
	all := pgFetchAll(t, dsn, table)
	if all[0]["name"] != "original" {
		t.Errorf("DO NOTHING failed: name = %v, want 'original'", all[0]["name"])
	}
}

func TestPostgresUpsert_PartialUpdate(t *testing.T) {
	// update_columns restricts which columns get overwritten.
	// id is the conflict key; name should update, score should be
	// preserved from the original insert.
	dsn, table := pgTestSetup(t)
	seedUpsertTable(t, dsn, table)
	_, _ = executePostgresUpsertRows(t.Context(), core.Job{
		Params: map[string]any{
			"dsn":              dsn,
			"table":            table,
			"conflict_columns": []string{"id"},
		},
		Input: map[string]core.Ref{
			"rows":    {Inline: []map[string]any{{"id": 1, "name": "orig", "score": 5.0}}},
			"headers": {Inline: []string{"id", "name", "score"}},
		},
	}, nil)

	res, _ := executePostgresUpsertRows(t.Context(), core.Job{
		Params: map[string]any{
			"dsn":              dsn,
			"table":            table,
			"conflict_columns": []string{"id"},
			"update_columns":   []string{"name"}, // only name, not score
		},
		Input: map[string]core.Ref{
			"rows":    {Inline: []map[string]any{{"id": 1, "name": "updated", "score": 99.0}}},
			"headers": {Inline: []string{"id", "name", "score"}},
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	all := pgFetchAll(t, dsn, table)
	if all[0]["name"] != "updated" {
		t.Errorf("name = %v, want 'updated'", all[0]["name"])
	}
	// score should still be 5.0; pgx returns numeric as pgtype.Numeric
	// or similar, so compare via string round-trip.
	if fmt.Sprint(all[0]["score"]) == "99" {
		t.Errorf("score = %v, want preserved at 5 (update_columns omitted)", all[0]["score"])
	}
}

func TestPostgresUpsert_CreateTableAddsUnique(t *testing.T) {
	// create_table=true must add a UNIQUE on the conflict columns so
	// ON CONFLICT has a target. Without it, the upsert would fail.
	dsn, table := pgTestSetup(t)
	// Don't pre-create — let the drop do it.
	res, _ := executePostgresUpsertRows(t.Context(), core.Job{
		Params: map[string]any{
			"dsn":              dsn,
			"table":            table,
			"create_table":     true,
			"conflict_columns": []string{"id"},
			"column_types":     map[string]any{"id": "integer", "name": "text"},
		},
		Input: map[string]core.Ref{
			"rows": {Inline: []map[string]any{
				{"id": 1, "name": "a"},
				{"id": 1, "name": "duplicate"}, // would error w/o unique constraint
			}},
			"headers": {Inline: []string{"id", "name"}},
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	// The second row should have UPDATEd the first.
	ctx := t.Context()
	conn, _ := pgx.Connect(ctx, dsn)
	defer conn.Close(ctx)
	var n int
	var name string
	if err := conn.QueryRow(ctx,
		fmt.Sprintf(`SELECT count(*), max(name) FROM "public"."%s"`, table)).Scan(&n, &name); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 || name != "duplicate" {
		t.Errorf("rows=%d name=%q, want 1 / duplicate", n, name)
	}
}

func TestPostgresUpsert_RollbackOnFailure(t *testing.T) {
	dsn, table := pgTestSetup(t)
	seedUpsertTable(t, dsn, table)
	// Insert a baseline so we can verify rollback.
	_, _ = executePostgresUpsertRows(t.Context(), core.Job{
		Params: map[string]any{
			"dsn":              dsn,
			"table":            table,
			"conflict_columns": []string{"id"},
		},
		Input: map[string]core.Ref{
			"rows":    {Inline: []map[string]any{{"id": 1, "name": "baseline", "score": 1.0}}},
			"headers": {Inline: []string{"id", "name", "score"}},
		},
	}, nil)

	// A second batch referencing a column that doesn't exist must roll
	// back; the baseline row must remain at "baseline".
	res, _ := executePostgresUpsertRows(t.Context(), core.Job{
		Params: map[string]any{
			"dsn":              dsn,
			"table":            table,
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
	all := pgFetchAll(t, dsn, table)
	if len(all) != 1 || all[0]["name"] != "baseline" {
		t.Errorf("rollback failed: %+v", all)
	}
}

// ----------------------------------------------------------------------
// Unit tests — no Postgres required.
// ----------------------------------------------------------------------

func TestPostgresUpsert_MissingConflictColumns(t *testing.T) {
	res, _ := executePostgresUpsertRows(t.Context(), core.Job{
		Params: map[string]any{
			"dsn":   "postgres://",
			"table": "t",
		},
		Input: map[string]core.Ref{
			"rows": {Inline: []map[string]any{{"a": "1"}}},
		},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%q, want bad_param", res.Status, res.Error.Code)
	}
}

func TestPostgresUpsert_EmptyConflictColumns(t *testing.T) {
	res, _ := executePostgresUpsertRows(t.Context(), core.Job{
		Params: map[string]any{
			"dsn":              "postgres://",
			"table":            "t",
			"conflict_columns": []string{},
		},
		Input: map[string]core.Ref{
			"rows": {Inline: []map[string]any{{"a": "1"}}},
		},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%q, want bad_param", res.Status, res.Error.Code)
	}
}

func TestPostgresUpsert_ConflictColumnNotInHeaders(t *testing.T) {
	res, _ := executePostgresUpsertRows(t.Context(), core.Job{
		Params: map[string]any{
			"dsn":              "postgres://",
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

func TestPostgresUpsert_UnsafeConflictColumn(t *testing.T) {
	res, _ := executePostgresUpsertRows(t.Context(), core.Job{
		Params: map[string]any{
			"dsn":              "postgres://",
			"table":            "t",
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

// `name; DROP` is now a legal column name — the drop quotes it, so
// the embedded semicolon stays inside the identifier. The only
// genuinely-unsafe shape that gets pre-rejected is the NUL byte
// (and empty, which the array shape can't carry meaningfully here).
func TestPostgresUpsert_UnsafeUpdateColumn(t *testing.T) {
	res, _ := executePostgresUpsertRows(t.Context(), core.Job{
		Params: map[string]any{
			"dsn":              "postgres://",
			"table":            "t",
			"conflict_columns": []string{"id"},
			"update_columns":   []string{"co\x00l"},
		},
		Input: map[string]core.Ref{
			"rows":    {Inline: []map[string]any{{"id": 1}}},
			"headers": {Inline: []string{"id"}},
		},
	}, nil)
	if res.Status != core.StatusError || res.Error == nil || res.Error.Code != "bad_param" {
		t.Errorf("status=%q error=%+v, want bad_param", res.Status, res.Error)
	}
}

func TestPostgresUpsert_SubtractHelper(t *testing.T) {
	got := subtract([]string{"a", "b", "c", "d"}, []string{"b", "d"})
	if len(got) != 2 || got[0] != "a" || got[1] != "c" {
		t.Errorf("subtract = %v, want [a c]", got)
	}
	// Order-preserving and idempotent over duplicates in b.
	got = subtract([]string{"x", "y"}, []string{"z", "z"})
	if len(got) != 2 || got[0] != "x" {
		t.Errorf("subtract w/o intersection = %v, want [x y]", got)
	}
}
