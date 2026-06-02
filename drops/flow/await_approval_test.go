package flow

import (
	"strings"
	"testing"

	"git.sr.ht/~klahr/hazyflow/core"
)

func TestAwaitApproval_ReturnsAwaitingSentinel(t *testing.T) {
	job := core.Job{
		ID:          "j1",
		ApprovalURL: "https://hzd.example/approve/run-1/node-A?token=tok",
	}
	res, err := executeAwaitApproval(t.Context(), job, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusAwaiting {
		t.Errorf("status = %q, want awaiting", res.Status)
	}
	url, _ := res.Output["pending_url"].Inline.(string)
	if !strings.Contains(url, "/approve/") {
		t.Errorf("pending_url = %q, want approval URL", url)
	}
}

func TestAwaitApproval_PassesContextThrough(t *testing.T) {
	payload := map[string]any{"invoice_id": "inv-99"}
	job := core.Job{
		ID:          "j2",
		ApprovalURL: "https://x/approve/r/n?token=t",
		Input: map[string]core.Ref{
			"context": {MIME: "application/json", Inline: payload},
		},
	}
	res, _ := executeAwaitApproval(t.Context(), job, nil)
	ctxRef := res.Output["context"]
	got, _ := ctxRef.Inline.(map[string]any)
	if got["invoice_id"] != "inv-99" {
		t.Errorf("context passthrough = %+v", got)
	}
}

func TestAwaitApproval_EmitsPromptWhenSet(t *testing.T) {
	job := core.Job{
		ID:          "j3",
		ApprovalURL: "https://x/a/r/n?token=t",
		Params:      map[string]any{"prompt": "Approve invoice over $10k?"},
	}
	res, _ := executeAwaitApproval(t.Context(), job, nil)
	if got := res.Output["prompt"].Inline; got != "Approve invoice over $10k?" {
		t.Errorf("prompt = %v", got)
	}
}
