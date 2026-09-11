// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package transform

import (
	"context"
	"testing"

	"github.com/dazyflow/dazyflow/core"
)

func runChunk(t *testing.T, size any, rows []map[string]any) core.Result {
	t.Helper()
	p := map[string]any{}
	if size != nil {
		p["size"] = size
	}
	res, err := executeChunkRows(context.Background(), core.Job{
		ID: "t", Params: p,
		Input: map[string]core.Ref{"rows": {MIME: "application/json", Inline: rows}},
	}, nil)
	if err != nil {
		t.Fatalf("executeChunkRows: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	return res
}

func rowsN(n int) []map[string]any {
	out := make([]map[string]any, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, map[string]any{"id": i})
	}
	return out
}

func batchesOf(t *testing.T, res core.Result) []map[string]any {
	t.Helper()
	raw, ok := res.Output["batches"].Inline.([]any)
	if !ok {
		t.Fatalf("batches is %T", res.Output["batches"].Inline)
	}
	out := make([]map[string]any, 0, len(raw))
	for _, b := range raw {
		m, ok := b.(map[string]any)
		if !ok {
			t.Fatalf("batch is %T, want an object", b)
		}
		out = append(out, m)
	}
	return out
}

func TestChunkRows_CutsIntoEqualBatchesWithARemainder(t *testing.T) {
	res := runChunk(t, 2, rowsN(5))
	got := batchesOf(t, res)
	if len(got) != 3 {
		t.Fatalf("got %d batches, want 3", len(got))
	}
	if got[0]["size"] != 2 || got[2]["size"] != 1 {
		t.Errorf("sizes = %v, %v, %v", got[0]["size"], got[1]["size"], got[2]["size"])
	}
	if res.Output["count"].Inline != "3" {
		t.Errorf("count = %v", res.Output["count"].Inline)
	}
}

// "part 3 of 10" without the flow counting anything itself.
func TestChunkRows_EachBatchKnowsItsPlace(t *testing.T) {
	got := batchesOf(t, runChunk(t, 2, rowsN(5)))
	for i, b := range got {
		if b["index"] != i || b["number"] != i+1 || b["of"] != 3 {
			t.Errorf("batch %d = index %v number %v of %v", i, b["index"], b["number"], b["of"])
		}
	}
}

// Every row survives the cut, in order.
func TestChunkRows_LosesNothing(t *testing.T) {
	got := batchesOf(t, runChunk(t, 3, rowsN(7)))
	seen := 0
	for _, b := range got {
		rows, ok := b["rows"].([]map[string]any)
		if !ok {
			t.Fatalf("batch rows are %T", b["rows"])
		}
		for _, r := range rows {
			if r["id"] != seen {
				t.Fatalf("row out of order: got %v at position %d", r["id"], seen)
			}
			seen++
		}
	}
	if seen != 7 {
		t.Errorf("saw %d rows, want 7", seen)
	}
}

func TestChunkRows_AnEmptyListMakesNoBatches(t *testing.T) {
	res := runChunk(t, 10, nil)
	if got := batchesOf(t, res); len(got) != 0 {
		t.Errorf("batches = %v, want none", got)
	}
	if res.Output["count"].Inline != "0" {
		t.Errorf("count = %v, want 0", res.Output["count"].Inline)
	}
}

// A size of zero would divide by nothing; the clamp is what stops it.
func TestChunkRows_ClampsASillySize(t *testing.T) {
	if got := batchesOf(t, runChunk(t, 0, rowsN(3))); len(got) != 3 {
		t.Errorf("size 0 made %d batches, want one row each", len(got))
	}
	if got := batchesOf(t, runChunk(t, 999999, rowsN(3))); len(got) != 1 {
		t.Errorf("a huge size made %d batches, want 1", len(got))
	}
}
