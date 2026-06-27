// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

func TestRedactResult_ScrubsOutputAndError(t *testing.T) {
	set := newSecretSet()
	set.add("sk_live_supersecret")

	result := core.Result{
		Status: core.StatusError,
		Output: map[string]core.Ref{
			"out": {
				Ref: "log-sk_live_supersecret.txt",
				Inline: map[string]any{
					"used":   "sk_live_supersecret",
					"note":   "called api with sk_live_supersecret today",
					"safe":   "hello world",
					"nested": []any{"x", "sk_live_supersecret", 42},
				},
			},
		},
		Error: &core.JobError{
			Code:    "upstream",
			Message: "auth failed using sk_live_supersecret",
			Details: "Authorization: Bearer sk_live_supersecret",
		},
	}

	redactResult(&result, set)

	blob, _ := json.Marshal(result)
	if strings.Contains(string(blob), "sk_live_supersecret") {
		t.Fatalf("secret survived redaction: %s", blob)
	}
	inline := result.Output["out"].Inline.(map[string]any)
	if inline["used"] != redactionMarker {
		t.Errorf("used = %v, want marker", inline["used"])
	}
	if inline["safe"] != "hello world" {
		t.Errorf("non-secret value was mangled: %v", inline["safe"])
	}
	if got := inline["nested"].([]any)[1]; got != redactionMarker {
		t.Errorf("nested secret = %v, want marker", got)
	}
	if got := inline["nested"].([]any)[2]; got != 42 {
		t.Errorf("non-string nested value changed: %v", got)
	}
}

func TestRedactResult_ShortSecretNotRedacted(t *testing.T) {
	set := newSecretSet()
	set.add("ab")  // below minRedactableSecretLen — would over-match
	set.add("123") // ditto

	result := core.Result{Output: map[string]core.Ref{
		"out": {Inline: map[string]any{"v": "ab123 and a number 123"}},
	}}
	redactResult(&result, set)

	// Nothing recorded (both too short), so the output is untouched —
	// short secrets fall back to the save-time lint.
	if got := result.Output["out"].Inline.(map[string]any)["v"]; got != "ab123 and a number 123" {
		t.Errorf("short-secret over-redaction: %v", got)
	}
}

func TestRunNode_RedactsLeakedSecret(t *testing.T) {
	const secret = "sk_live_supersecrettoken"
	e := newEngineWith(t, NativeDrop{
		Manifest: core.Manifest{
			ID:       "echo",
			Summary:  "Test fixture echo.",
			Examples: []core.ParamsExample{{Title: "default"}},
			Inputs:   []core.Port{{Port: "in"}},
			Outputs:  []core.Port{{Port: "out"}},
		},
		// A misbehaving module that echoes its resolved param straight
		// into its output and error — the exact leak shape redaction
		// must catch.
		Execute: func(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
			tok, _ := job.Params["token"].(string)
			return core.Result{
				Status: core.StatusError,
				Output: map[string]core.Ref{
					"out": {Inline: map[string]any{"echoed": tok}},
				},
				Error: &core.JobError{Code: "boom", Message: "failed with " + tok},
			}, nil
		},
	})
	e.Secrets = newProviders(stubProvider{
		scheme: "secret",
		values: map[string]string{"api": secret},
	})

	g := core.Graph{
		ID:     "g",
		Tenant: "acme",
		Nodes: []core.Node{
			{ID: "n", Module: "echo", Params: map[string]any{"token": "${secret.api}"}},
		},
	}
	res, err := e.RunNode(t.Context(), g, "run1", "n", "rec1", nil, nil)
	if err != nil {
		t.Fatalf("RunNode: %v", err)
	}
	blob, _ := json.Marshal(res)
	if strings.Contains(string(blob), secret) {
		t.Fatalf("secret leaked through RunNode into persisted result: %s", blob)
	}
	if got := res.Output["out"].Inline.(map[string]any)["echoed"]; got != redactionMarker {
		t.Errorf("echoed output = %v, want marker", got)
	}
}
