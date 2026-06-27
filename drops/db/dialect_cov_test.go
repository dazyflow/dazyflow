package db

import (
	"context"
	"errors"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

// fakeConn is an in-memory conn stub for exercising the shared run* skeletons
// without a real database. Each method returns canned results or errors so the
// happy and error branches can be driven deterministically.
type fakeConn struct {
	execErr  error
	queryErr error
	cols     []string
	rows     []map[string]any
	batchN   int
	batchErr error

	// captured for assertions
	lastExec  string
	lastBatch string
}

func (c *fakeConn) exec(_ context.Context, sql string) error {
	c.lastExec = sql
	return c.execErr
}

func (c *fakeConn) query(_ context.Context, _ string, _ []any, _ int) ([]string, []map[string]any, error) {
	if c.queryErr != nil {
		return nil, nil, c.queryErr
	}
	return c.cols, c.rows, nil
}

func (c *fakeConn) execBatch(_ context.Context, stmt string, _ []string, _ []map[string]any, _ string) (int, error) {
	c.lastBatch = stmt
	return c.batchN, c.batchErr
}

// TestRunQuery covers the param-parsing wrapper and the OK path through a
// stub conn. (No drop calls runQuery directly, so this is its only coverage.)
func TestRunQuery(t *testing.T) {
	t.Run("bad params short-circuit before conn", func(t *testing.T) {
		res, err := runQuery(t.Context(), core.Job{Params: map[string]any{}}, &fakeConn{})
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if res.Status != core.StatusError || res.Error.Code != "bad_param" {
			t.Fatalf("want bad_param, got status=%q err=%+v", res.Status, res.Error)
		}
	})
	t.Run("ok path emits rows + columns", func(t *testing.T) {
		c := &fakeConn{cols: []string{"id"}, rows: []map[string]any{{"id": 1}}}
		res, err := runQuery(t.Context(), core.Job{Params: map[string]any{"sql": "SELECT 1"}}, c)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if res.Status != core.StatusOK {
			t.Fatalf("status=%q err=%+v", res.Status, res.Error)
		}
		if cols, _ := res.Output["columns"].Inline.([]string); len(cols) != 1 || cols[0] != "id" {
			t.Errorf("columns = %v", res.Output["columns"].Inline)
		}
	})
}

// TestRunQueryParsed_Errors covers the two error mappings: too_many_rows
// (the sentinel) and a generic db error.
func TestRunQueryParsed_Errors(t *testing.T) {
	t.Run("too many rows maps to too_many_rows", func(t *testing.T) {
		c := &fakeConn{queryErr: errTooManyRows}
		res, _ := runQueryParsed(t.Context(), core.Job{}, c, queryParams{sql: "SELECT 1"})
		if res.Status != core.StatusError || res.Error.Code != "too_many_rows" {
			t.Fatalf("want too_many_rows, got status=%q err=%+v", res.Status, res.Error)
		}
	})
	t.Run("other error maps to db", func(t *testing.T) {
		c := &fakeConn{queryErr: errors.New("boom")}
		res, _ := runQueryParsed(t.Context(), core.Job{}, c, queryParams{sql: "SELECT 1"})
		if res.Status != core.StatusError || res.Error.Code != "db" {
			t.Fatalf("want db, got status=%q err=%+v", res.Status, res.Error)
		}
	})
}

// TestRunInsert covers create-table error, column_types error, zero-rows
// early return, batch error, and the OK count path.
func TestRunInsert(t *testing.T) {
	d := sqliteDialect{}
	ri := rowsInput{rows: []map[string]any{{"a": 1}}, headers: []string{"a"}}

	t.Run("column_types parse error", func(t *testing.T) {
		job := core.Job{Params: map[string]any{"column_types": map[string]any{"a": "NOT A TYPE; DROP"}}}
		res, _ := runInsert(t.Context(), job, d, &fakeConn{}, `"t"`, ri)
		if res.Status != core.StatusError || res.Error.Code != "db" {
			t.Fatalf("want db, got status=%q err=%+v", res.Status, res.Error)
		}
	})
	t.Run("create table exec error", func(t *testing.T) {
		res, _ := runInsert(t.Context(), core.Job{}, d, &fakeConn{execErr: errors.New("no")}, `"t"`, ri)
		if res.Status != core.StatusError || res.Error.Code != "db" {
			t.Fatalf("want db, got status=%q err=%+v", res.Status, res.Error)
		}
	})
	t.Run("zero rows returns inserted=0 without batch", func(t *testing.T) {
		// create_table=false so no exec; empty rows → early return.
		job := core.Job{Params: map[string]any{"create_table": false}}
		res, _ := runInsert(t.Context(), job, d, &fakeConn{}, `"t"`, rowsInput{headers: []string{"a"}})
		if res.Status != core.StatusOK {
			t.Fatalf("status=%q err=%+v", res.Status, res.Error)
		}
		if n, _ := res.Output["inserted"].Inline.(int); n != 0 {
			t.Errorf("inserted = %v, want 0", res.Output["inserted"].Inline)
		}
	})
	t.Run("batch error maps to db", func(t *testing.T) {
		job := core.Job{Params: map[string]any{"create_table": false}}
		res, _ := runInsert(t.Context(), job, d, &fakeConn{batchErr: errors.New("dup")}, `"t"`, ri)
		if res.Status != core.StatusError || res.Error.Code != "db" {
			t.Fatalf("want db, got status=%q err=%+v", res.Status, res.Error)
		}
	})
	t.Run("ok path returns count", func(t *testing.T) {
		job := core.Job{Params: map[string]any{"create_table": false}}
		res, _ := runInsert(t.Context(), job, d, &fakeConn{batchN: 3}, `"t"`, ri)
		if res.Status != core.StatusOK {
			t.Fatalf("status=%q err=%+v", res.Status, res.Error)
		}
		if n, _ := res.Output["inserted"].Inline.(int); n != 3 {
			t.Errorf("inserted = %v, want 3", res.Output["inserted"].Inline)
		}
	})
}

// TestRunUpsert covers the column_types error, create error, zero-rows path,
// the derive-update-cols branch, the explicit-update-cols branch, and a
// batch error.
func TestRunUpsert(t *testing.T) {
	d := postgresDialect{}
	ri := rowsInput{rows: []map[string]any{{"id": 1, "name": "x"}}, headers: []string{"id", "name"}}

	t.Run("column_types parse error", func(t *testing.T) {
		job := core.Job{Params: map[string]any{"column_types": map[string]any{"id": "bogus;;"}}}
		res, _ := runUpsert(t.Context(), job, d, &fakeConn{}, `"t"`, ri, []string{"id"}, nil, false)
		if res.Status != core.StatusError || res.Error.Code != "db" {
			t.Fatalf("want db, got status=%q err=%+v", res.Status, res.Error)
		}
	})
	t.Run("create table exec error", func(t *testing.T) {
		res, _ := runUpsert(t.Context(), core.Job{}, d, &fakeConn{execErr: errors.New("no")}, `"t"`, ri, []string{"id"}, nil, false)
		if res.Status != core.StatusError || res.Error.Code != "db" {
			t.Fatalf("want db, got status=%q err=%+v", res.Status, res.Error)
		}
	})
	t.Run("zero rows returns processed=0", func(t *testing.T) {
		job := core.Job{Params: map[string]any{"create_table": false}}
		res, _ := runUpsert(t.Context(), job, d, &fakeConn{}, `"t"`, rowsInput{headers: []string{"id"}}, []string{"id"}, nil, false)
		if res.Status != core.StatusOK {
			t.Fatalf("status=%q err=%+v", res.Status, res.Error)
		}
		if n, _ := res.Output["processed"].Inline.(int); n != 0 {
			t.Errorf("processed = %v, want 0", res.Output["processed"].Inline)
		}
	})
	t.Run("derives update cols when not explicit", func(t *testing.T) {
		job := core.Job{Params: map[string]any{"create_table": false}}
		c := &fakeConn{batchN: 1}
		res, _ := runUpsert(t.Context(), job, d, c, `"t"`, ri, []string{"id"}, nil, false)
		if res.Status != core.StatusOK {
			t.Fatalf("status=%q err=%+v", res.Status, res.Error)
		}
		// Derived update set = headers \ conflict = {name}; statement should
		// reference EXCLUDED."name".
		if c.lastBatch == "" || !strings.Contains(c.lastBatch, `"name" = EXCLUDED."name"`) {
			t.Errorf("derived upsert clause wrong: %q", c.lastBatch)
		}
	})
	t.Run("explicit empty update cols → DO NOTHING", func(t *testing.T) {
		job := core.Job{Params: map[string]any{"create_table": false}}
		c := &fakeConn{batchN: 1}
		res, _ := runUpsert(t.Context(), job, d, c, `"t"`, ri, []string{"id"}, nil, true)
		if res.Status != core.StatusOK {
			t.Fatalf("status=%q err=%+v", res.Status, res.Error)
		}
		if !strings.Contains(c.lastBatch, "DO NOTHING") {
			t.Errorf("explicit empty should be DO NOTHING: %q", c.lastBatch)
		}
	})
	t.Run("batch error maps to db", func(t *testing.T) {
		job := core.Job{Params: map[string]any{"create_table": false}}
		res, _ := runUpsert(t.Context(), job, d, &fakeConn{batchErr: errors.New("x")}, `"t"`, ri, []string{"id"}, nil, false)
		if res.Status != core.StatusError || res.Error.Code != "db" {
			t.Fatalf("want db, got status=%q err=%+v", res.Status, res.Error)
		}
	})
}

// TestQueryGuard covers the limit-reached stop and the row-ceiling error in
// isolation (the shared append-with-bounds helper).
func TestQueryGuard(t *testing.T) {
	t.Run("limit reached signals stop", func(t *testing.T) {
		out, stop, err := queryGuard([]map[string]any{{"a": 1}}, map[string]any{"a": 2}, 2)
		if err != nil || !stop || len(out) != 2 {
			t.Fatalf("got (len=%d, stop=%v, err=%v), want stop", len(out), stop, err)
		}
	})
	t.Run("under limit continues", func(t *testing.T) {
		out, stop, err := queryGuard(nil, map[string]any{"a": 1}, 5)
		if err != nil || stop || len(out) != 1 {
			t.Fatalf("got (len=%d, stop=%v, err=%v)", len(out), stop, err)
		}
	})
}
