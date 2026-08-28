// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package db

import (
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

// TestParseConflictUpdateCols covers each branch of the upsert column parser.
func TestParseConflictUpdateCols(t *testing.T) {
	t.Run("missing conflict_columns errors", func(t *testing.T) {
		_, _, _, errRes := parseConflictUpdateCols(core.Job{Params: map[string]any{}})
		if errRes == nil || errRes.Error.Code != "bad_param" {
			t.Fatalf("want bad_param, got %+v", errRes)
		}
	})
	t.Run("empty conflict_columns errors", func(t *testing.T) {
		_, _, _, errRes := parseConflictUpdateCols(core.Job{Params: map[string]any{"conflict_columns": []any{}}})
		if errRes == nil || errRes.Error.Code != "bad_param" {
			t.Fatalf("want bad_param, got %+v", errRes)
		}
	})
	t.Run("invalid conflict column errors", func(t *testing.T) {
		_, _, _, errRes := parseConflictUpdateCols(core.Job{Params: map[string]any{"conflict_columns": []any{"bad\x00col"}}})
		if errRes == nil || errRes.Error.Code != "bad_param" {
			t.Fatalf("want bad_param, got %+v", errRes)
		}
	})
	t.Run("update_columns wrong type errors", func(t *testing.T) {
		_, _, _, errRes := parseConflictUpdateCols(core.Job{Params: map[string]any{
			"conflict_columns": []any{"id"},
			"update_columns":   "not-an-array",
		}})
		if errRes == nil || errRes.Error.Code != "bad_param" {
			t.Fatalf("want bad_param, got %+v", errRes)
		}
	})
	t.Run("invalid update column errors", func(t *testing.T) {
		_, _, _, errRes := parseConflictUpdateCols(core.Job{Params: map[string]any{
			"conflict_columns": []any{"id"},
			"update_columns":   []any{"bad\x00col"},
		}})
		if errRes == nil || errRes.Error.Code != "bad_param" {
			t.Fatalf("want bad_param, got %+v", errRes)
		}
	})
	t.Run("explicit empty update_columns → explicit=true, nil cols", func(t *testing.T) {
		conflict, update, explicit, errRes := parseConflictUpdateCols(core.Job{Params: map[string]any{
			"conflict_columns": []any{"id"},
			"update_columns":   []any{},
		}})
		if errRes != nil {
			t.Fatalf("unexpected error: %+v", errRes)
		}
		if !explicit || len(update) != 0 || len(conflict) != 1 {
			t.Fatalf("got conflict=%v update=%v explicit=%v", conflict, update, explicit)
		}
	})
	t.Run("absent update_columns → explicit=false", func(t *testing.T) {
		_, _, explicit, errRes := parseConflictUpdateCols(core.Job{Params: map[string]any{
			"conflict_columns": []any{"id"},
		}})
		if errRes != nil || explicit {
			t.Fatalf("want explicit=false no error, got explicit=%v err=%+v", explicit, errRes)
		}
	})
	t.Run("valid explicit update_columns", func(t *testing.T) {
		conflict, update, explicit, errRes := parseConflictUpdateCols(core.Job{Params: map[string]any{
			"conflict_columns": []any{"id"},
			"update_columns":   []any{"name", "email"},
		}})
		if errRes != nil || !explicit || len(update) != 2 || conflict[0] != "id" {
			t.Fatalf("got conflict=%v update=%v explicit=%v err=%+v", conflict, update, explicit, errRes)
		}
	})
}

// TestCheckConflictInHeaders covers the present / missing branches.
func TestCheckConflictInHeaders(t *testing.T) {
	t.Run("all present → nil", func(t *testing.T) {
		if r := checkConflictInHeaders(core.Job{}, []string{"id"}, []string{"id", "name"}); r != nil {
			t.Fatalf("want nil, got %+v", r)
		}
	})
	t.Run("missing conflict col → bad_param", func(t *testing.T) {
		r := checkConflictInHeaders(core.Job{}, []string{"missing"}, []string{"id"})
		if r == nil || r.Error.Code != "bad_param" {
			t.Fatalf("want bad_param, got %+v", r)
		}
	})
}

// TestParseRowsInput_MissingAndBad covers the missing-input and bad-input
// branches of the shared row parser.
func TestParseRowsInput_MissingAndBad(t *testing.T) {
	t.Run("missing rows port", func(t *testing.T) {
		_, errRes := parseRowsInput(core.Job{})
		if errRes == nil || errRes.Error.Code != "missing_input" {
			t.Fatalf("want missing_input, got %+v", errRes)
		}
	})
	t.Run("unsupported rows shape", func(t *testing.T) {
		_, errRes := parseRowsInput(core.Job{Input: map[string]core.Ref{"rows": {Inline: 42}}})
		if errRes == nil || errRes.Error.Code != "bad_input" {
			t.Fatalf("want bad_input, got %+v", errRes)
		}
	})
	t.Run("bad header identifier", func(t *testing.T) {
		_, errRes := parseRowsInput(core.Job{Input: map[string]core.Ref{
			"rows": {Inline: []map[string]any{{"bad\x00col": 1}}},
		}})
		if errRes == nil || errRes.Error.Code != "bad_input" {
			t.Fatalf("want bad_input, got %+v", errRes)
		}
	})
}
