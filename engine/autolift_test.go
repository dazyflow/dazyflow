// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"reflect"
	"testing"

	"github.com/dazyflow/dazyflow/core"
)

// TestAutoLiftToList covers the one→many rule: a single value feeding a MANY
// (list) input port is wrapped into a one-element list, while lists, blob refs,
// empties, and one-input ports are left untouched.
func TestAutoLiftToList(t *testing.T) {
	listPort := core.Port{Port: "rows", List: true}
	onePort := core.Port{Port: "body"}

	t.Run("single object into list port is wrapped", func(t *testing.T) {
		ref := core.Ref{Inline: map[string]any{"name": "Asha"}}
		got := autoLiftToList(listPort, ref)
		lst, ok := got.Inline.([]any)
		if !ok || len(lst) != 1 {
			t.Fatalf("expected a 1-element []any, got %#v", got.Inline)
		}
		if !reflect.DeepEqual(lst[0], map[string]any{"name": "Asha"}) {
			t.Fatalf("wrapped element wrong: %#v", lst[0])
		}
	})

	t.Run("scalar into list port is wrapped", func(t *testing.T) {
		got := autoLiftToList(listPort, core.Ref{Inline: "hello"})
		if lst, ok := got.Inline.([]any); !ok || len(lst) != 1 || lst[0] != "hello" {
			t.Fatalf("expected [\"hello\"], got %#v", got.Inline)
		}
	})

	t.Run("already a list is untouched", func(t *testing.T) {
		orig := []any{map[string]any{"a": 1}, map[string]any{"a": 2}}
		got := autoLiftToList(listPort, core.Ref{Inline: orig})
		if !reflect.DeepEqual(got.Inline, orig) {
			t.Fatalf("a list must not be re-wrapped: %#v", got.Inline)
		}
	})

	t.Run("typed slice is untouched (no double-wrap)", func(t *testing.T) {
		orig := []map[string]any{{"a": 1}}
		got := autoLiftToList(listPort, core.Ref{Inline: orig})
		if reflect.TypeOf(got.Inline).Kind() != reflect.Slice {
			t.Fatalf("typed slice should pass through as a slice, got %#v", got.Inline)
		}
		if l := reflect.ValueOf(got.Inline).Len(); l != 1 {
			t.Fatalf("typed slice should keep len 1, got %d", l)
		}
	})

	t.Run("one-input port is untouched", func(t *testing.T) {
		got := autoLiftToList(onePort, core.Ref{Inline: map[string]any{"x": 1}})
		if _, isList := got.Inline.([]any); isList {
			t.Fatal("a non-list input port must not wrap its value")
		}
	})

	t.Run("blob ref and empty are untouched", func(t *testing.T) {
		blob := core.Ref{Ref: "blob://abc", MIME: "application/json"}
		if got := autoLiftToList(listPort, blob); got.Inline != nil {
			t.Fatalf("blob ref must pass through, got %#v", got.Inline)
		}
	})
}
