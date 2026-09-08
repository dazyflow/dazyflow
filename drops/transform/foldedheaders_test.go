// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package transform

import (
	"testing"

	"github.com/dazyflow/dazyflow/core"
)

// Proves the folded-headers model: column order travels ON the rows value
// (Ref.Headers), so a consumer gets it with no separate `headers` input wired.
func TestFoldedHeaders_ReadFromRowsRef(t *testing.T) {
	job := core.Job{
		ID: "j",
		Input: map[string]core.Ref{
			"rows": {
				MIME:    "application/json",
				Inline:  []map[string]any{{"b": 2, "a": 1}},
				Headers: []string{"b", "a"}, // explicit order, NOT alphabetical
			},
		},
	}
	rows, headers, _, ok := loadRowsAndHeaders(job)
	if !ok {
		t.Fatal("loadRowsAndHeaders failed")
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	if len(headers) != 2 || headers[0] != "b" || headers[1] != "a" {
		t.Fatalf("headers should come from the rows Ref in order [b a], got %v", headers)
	}
}

// TestFoldedHeaders_DerivedWhenValueCarriesNone: no `headers` input exists to
// fall back to, so an order the value doesn't carry is derived from the row
// keys — alphabetically, which is what makes it stable.
func TestFoldedHeaders_DerivedWhenValueCarriesNone(t *testing.T) {
	job := core.Job{
		ID: "j",
		Input: map[string]core.Ref{
			"rows": {Inline: []map[string]any{{"b": 2, "a": 1}}},
		},
	}
	_, headers, _, ok := loadRowsAndHeaders(job)
	if !ok || len(headers) != 2 || headers[0] != "a" || headers[1] != "b" {
		t.Fatalf("headers should be derived as [a b], got %v (ok=%v)", headers, ok)
	}
}

// Proves resultRows attaches the column order to the rows Ref (so downstream
// reads it from the value).
func TestFoldedHeaders_OutputCarriesHeaders(t *testing.T) {
	res := resultRows(core.Job{ID: "j"}, []map[string]any{{"a": 1}}, []string{"a", "z"})
	got := res.Output["rows"]
	if len(got.Headers) != 2 || got.Headers[0] != "a" || got.Headers[1] != "z" {
		t.Fatalf("rows Ref should carry Headers [a z], got %v", got.Headers)
	}
}
