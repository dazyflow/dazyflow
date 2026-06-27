// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package transform

import (
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/limits"
)

// TestNormalizeRows_RejectsOversizedInput proves the shared row reader refuses
// an input list larger than the ceiling — the chokepoint every transform drop
// goes through — so no transform can be made to hold an unbounded list.
func TestNormalizeRows_RejectsOversizedInput(t *testing.T) {
	defer limits.SetMaxRows(3)()

	bigAny := make([]any, 5)
	bigMaps := make([]map[string]any, 5)
	for i := range bigAny {
		bigAny[i] = map[string]any{"id": i}
		bigMaps[i] = map[string]any{"id": i}
	}
	if _, err := normalizeRows(bigAny); err == nil {
		t.Error("normalizeRows accepted 5 ([]any) rows under a 3-row limit")
	}
	if _, err := normalizeRows(bigMaps); err == nil {
		t.Error("normalizeRows accepted 5 ([]map) rows under a 3-row limit")
	}

	atLimit := make([]any, 3)
	for i := range atLimit {
		atLimit[i] = map[string]any{"id": i}
	}
	if _, err := normalizeRows(atLimit); err != nil {
		t.Errorf("normalizeRows rejected 3 rows at a 3-row limit: %v", err)
	}
}

// TestJoinRows_ManyToManyOutputCapped proves the join's output guard fires:
// both sides share one key, so a 4×4 inner join cartesians to 16 rows. With a
// 5-row ceiling — and inputs (4 each) under the input cap — only the OUTPUT
// guard can catch this, and it must, rather than building the full product.
func TestJoinRows_ManyToManyOutputCapped(t *testing.T) {
	defer limits.SetMaxRows(5)()

	left := make([]map[string]any, 4)
	right := make([]map[string]any, 4)
	for i := range left {
		left[i] = map[string]any{"k": "same", "l": i}
		right[i] = map[string]any{"k": "same", "r": i}
	}
	job := core.Job{
		ID:     "x",
		Params: map[string]any{"on": map[string]any{"k": "k"}, "kind": "inner"},
		Input: map[string]core.Ref{
			"left_rows":  {Inline: left},
			"right_rows": {Inline: right},
		},
	}
	res, err := executeJoinRows(t.Context(), job, nil)
	if err != nil {
		t.Fatalf("execute err: %v", err)
	}
	if res.Status != core.StatusError || res.Error == nil || res.Error.Code != "too_large" {
		t.Fatalf("expected too_large error, got status=%q err=%+v", res.Status, res.Error)
	}
}
