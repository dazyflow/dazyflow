// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package db

import (
	"reflect"
	"sort"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

func TestApplyFieldMapping(t *testing.T) {
	rows := []map[string]any{
		{"First name": "Alice", "Email address": "a@x.com", "SSN": "111"},
		{"First name": "Bob", "Email address": "b@x.com", "SSN": "222"},
	}

	t.Run("select + rename, drops unmapped", func(t *testing.T) {
		got := applyFieldMapping(rows, map[string]string{
			"First name":    "name",
			"Email address": "email",
		})
		want := []map[string]any{
			{"name": "Alice", "email": "a@x.com"},
			{"name": "Bob", "email": "b@x.com"},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("blank target skips the field", func(t *testing.T) {
		got := applyFieldMapping(rows, map[string]string{
			"Email address": "email",
			"SSN":           "  ", // blank → drop
		})
		if len(got[0]) != 1 {
			t.Fatalf("expected 1 column, got %v", got[0])
		}
		if _, ok := got[0]["email"]; !ok {
			t.Errorf("email missing: %v", got[0])
		}
	})

	t.Run("missing input field yields no key (not nil value)", func(t *testing.T) {
		got := applyFieldMapping(rows, map[string]string{"Nope": "x"})
		if len(got[0]) != 0 {
			t.Errorf("expected empty row for absent source field, got %v", got[0])
		}
	})
}

// TestParseRowsInput_FieldMapping confirms the mapper runs inside the shared
// parse path: headers are re-derived from the OUTPUT columns and validated, so
// a mapped column name still goes through identifier validation.
func TestParseRowsInput_FieldMapping(t *testing.T) {
	job := core.Job{
		Params: map[string]any{
			"field_mapping": map[string]any{
				"First name":    "name",
				"Email address": "email",
			},
		},
		Input: map[string]core.Ref{
			"rows": {Inline: []any{
				map[string]any{"First name": "Alice", "Email address": "a@x.com", "SSN": "111"},
			}},
		},
	}
	ri, errRes := parseRowsInput(job)
	if errRes != nil {
		t.Fatalf("unexpected error: %+v", *errRes)
	}
	got := append([]string(nil), ri.headers...)
	sort.Strings(got)
	want := []string{"email", "name"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("headers = %v, want %v (SSN should be dropped)", got, want)
	}
	if len(ri.rows) != 1 || ri.rows[0]["name"] != "Alice" {
		t.Fatalf("rows not mapped: %v", ri.rows)
	}
}
