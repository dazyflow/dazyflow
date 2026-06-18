package value

import (
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

func TestText_EmitsParam(t *testing.T) {
	res, err := executeText(t.Context(), core.Job{
		Params: map[string]any{"text": "hello world"},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	out := res.Output["out"]
	if out.MIME != "text/plain" {
		t.Errorf("MIME = %q, want text/plain", out.MIME)
	}
	if got, _ := out.Inline.(string); got != "hello world" {
		t.Errorf("Inline = %q, want %q", got, "hello world")
	}
}

func TestText_Multiline(t *testing.T) {
	body := "line one\nline two\nline three"
	res, _ := executeText(t.Context(), core.Job{
		Params: map[string]any{"text": body},
	}, nil)
	if got, _ := res.Output["out"].Inline.(string); got != body {
		t.Errorf("multiline preserved? got %q", got)
	}
}

func TestText_EmptyAllowed(t *testing.T) {
	// An empty string is still a valid value — useful as a "null"
	// placeholder downstream. The schema marks 'text' as required so
	// the param is always present; absent => empty.
	res, _ := executeText(t.Context(), core.Job{
		Params: map[string]any{"text": ""},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
}
