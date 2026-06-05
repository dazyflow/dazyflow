package flow

import (
	"testing"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/drops/internal/limits"
)

// TestForEach_RejectsOversizedItemsList proves for_each refuses an items list
// bigger than the ceiling before allocating a per-item result slot or spawning
// a sub-job for each — i.e. fails fast rather than fanning out unboundedly.
func TestForEach_RejectsOversizedItemsList(t *testing.T) {
	defer limits.SetMaxRows(3)()

	if _, err := normalizeItems(core.Ref{Inline: []any{1, 2, 3, 4, 5}}); err == nil {
		t.Error("normalizeItems accepted 5 items under a 3-item limit")
	}

	job := core.Job{
		ID:     "x",
		Params: map[string]any{"step_module": "noop"},
		Input:  map[string]core.Ref{"items": {Inline: []any{1, 2, 3, 4, 5}}},
	}
	res, err := executeForEach(t.Context(), job, nil)
	if err != nil {
		t.Fatalf("execute err: %v", err)
	}
	if res.Status != core.StatusError || res.Error == nil {
		t.Fatalf("expected error result for oversized items, got status=%q err=%+v", res.Status, res.Error)
	}
}
