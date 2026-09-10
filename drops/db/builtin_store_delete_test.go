// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package db

import (
	"strings"
	"testing"

	"github.com/dazyflow/dazyflow/core"
	_ "modernc.org/sqlite"
)

func seed(t *testing.T, root string, rows []map[string]any) {
	t.Helper()
	res, err := executeBuiltinStoreAppend(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"table": "orders"},
		Input:         map[string]core.Ref{"rows": {Inline: rows}},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("seed: %v %+v", err, res.Error)
	}
}

func remaining(t *testing.T, root string) []map[string]any {
	t.Helper()
	res, err := executeBuiltinStoreFind(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"table": "orders"},
	}, nil)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("find status=%q err=%+v", res.Status, res.Error)
	}
	rows, _ := res.Output["rows"].Inline.([]map[string]any)
	return rows
}

func deleteRows(t *testing.T, root string, p map[string]any) core.Result {
	t.Helper()
	p["table"] = "orders"
	res, err := executeBuiltinStoreDelete(t.Context(), core.Job{WorkspaceRoot: root, Params: p}, nil)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	return res
}

func TestStoreDelete_RemovesOnlyTheMatches(t *testing.T) {
	root := t.TempDir()
	seed(t, root, []map[string]any{
		{"id": "a", "status": "done"},
		{"id": "b", "status": "open"},
		{"id": "c", "status": "done"},
	})

	res := deleteRows(t, root, map[string]any{"filter": `row.status == "done"`})
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if got, _ := res.Output["deleted"].Inline.(int); got != 2 {
		t.Errorf("deleted = %v, want 2", res.Output["deleted"].Inline)
	}
	left := remaining(t, root)
	if len(left) != 1 || left[0]["id"] != "b" {
		t.Errorf("left = %v, want only the open one", left)
	}
	// The key the delete runs on is an artefact of the query, not a column
	// the collection grew.
	if _, leaked := left[0][rowidAlias]; leaked {
		t.Errorf("the row key leaked into the collection: %v", left[0])
	}
}

// Emptying a collection has to be said out loud.
func TestStoreDelete_WontEmptyWithoutSayingSo(t *testing.T) {
	root := t.TempDir()
	seed(t, root, []map[string]any{{"id": "a"}})

	res := deleteRows(t, root, map[string]any{})
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Fatalf("status=%q err=%+v, want bad_param", res.Status, res.Error)
	}
	if !strings.Contains(res.Error.Message, "Delete every row") {
		t.Errorf("message = %q, want it to name the switch", res.Error.Message)
	}
	if len(remaining(t, root)) != 1 {
		t.Error("the refused delete still removed rows")
	}

	if got := deleteRows(t, root, map[string]any{"all": true}); got.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", got.Status, got.Error)
	}
	if left := remaining(t, root); len(left) != 0 {
		t.Errorf("left = %v, want none", left)
	}
}

// The collection itself survives, so the flow that fills it keeps working.
func TestStoreDelete_KeepsTheCollection(t *testing.T) {
	root := t.TempDir()
	seed(t, root, []map[string]any{{"id": "a"}})
	deleteRows(t, root, map[string]any{"all": true})
	seed(t, root, []map[string]any{{"id": "b"}})

	if left := remaining(t, root); len(left) != 1 || left[0]["id"] != "b" {
		t.Errorf("left = %v, want the row saved after the emptying", left)
	}
}

func TestStoreDelete_MatchingNothingIsFine(t *testing.T) {
	root := t.TempDir()
	seed(t, root, []map[string]any{{"id": "a", "status": "open"}})

	res := deleteRows(t, root, map[string]any{"filter": `row.status == "done"`})
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if got, _ := res.Output["deleted"].Inline.(int); got != 0 {
		t.Errorf("deleted = %v, want 0", res.Output["deleted"].Inline)
	}
	if len(remaining(t, root)) != 1 {
		t.Error("a filter that matched nothing removed a row")
	}
}

// A filter that blows up must leave the collection exactly as it was.
func TestStoreDelete_ABrokenFilterChangesNothing(t *testing.T) {
	root := t.TempDir()
	seed(t, root, []map[string]any{{"id": "a", "status": "open"}})

	res := deleteRows(t, root, map[string]any{"filter": `row.status.nope()`})
	if res.Status != core.StatusError {
		t.Fatalf("status=%q, want an error", res.Status)
	}
	if len(remaining(t, root)) != 1 {
		t.Error("a failed delete still removed rows")
	}
}

func TestStoreDelete_UnknownCollection(t *testing.T) {
	root := t.TempDir()
	seed(t, root, []map[string]any{{"id": "a"}})

	res, err := executeBuiltinStoreDelete(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"table": "nope", "all": true},
	}, nil)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if res.Status != core.StatusError || res.Error.Code != "no_such_collection" {
		t.Fatalf("status=%q err=%+v, want no_such_collection", res.Status, res.Error)
	}
}

// Nothing was ever saved, so there is nothing to delete — and no store file to
// create just to say so.
func TestStoreDelete_NoStoreAtAll(t *testing.T) {
	res := deleteRows(t, t.TempDir(), map[string]any{"all": true})
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if got, _ := res.Output["deleted"].Inline.(int); got != 0 {
		t.Errorf("deleted = %v, want 0", res.Output["deleted"].Inline)
	}
}
