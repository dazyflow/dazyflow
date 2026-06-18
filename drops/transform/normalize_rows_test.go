package transform

import (
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

// These pin the trigger-body coercions: a webhook/form JSON object body
// arrives as a single map (or a JSON string), and wiring it straight
// into a transform's rows port must work — it's the most common starter
// shape in the template gallery.

func TestNormalizeRows_SingleObject(t *testing.T) {
	got, err := normalizeRows(map[string]any{"a": "1", "b": int64(2)})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(got) != 1 || got[0]["a"] != "1" {
		t.Errorf("got %+v, want one row", got)
	}
}

func TestNormalizeRows_StringObject(t *testing.T) {
	got, err := normalizeRows(`{"name":"ada"}`)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(got) != 1 || got[0]["name"] != "ada" {
		t.Errorf("got %+v", got)
	}
}

func TestNormalizeRows_StringArray(t *testing.T) {
	got, err := normalizeRows(`[{"n":1},{"n":2}]`)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("got %d rows, want 2", len(got))
	}
}

func TestNormalizeRows_EmptyStringIsNoRows(t *testing.T) {
	got, err := normalizeRows("")
	if err != nil || got != nil {
		t.Errorf("got %+v err %v, want nil/nil", got, err)
	}
}

// End-to-end through render_text: a single-object webhook body renders.
func TestRenderText_AcceptsSingleObjectBody(t *testing.T) {
	res, _ := executeRenderText(t.Context(), core.Job{
		Params: map[string]any{"template": "'issue: ' + row.title"},
		Input:  map[string]core.Ref{"rows": {Inline: map[string]any{"title": "bug"}}},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if res.Output["text"].Inline.(string) != "issue: bug" {
		t.Errorf("got %q", res.Output["text"].Inline)
	}
}
