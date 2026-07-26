// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

// TestRedactResult_ScrubsHeaders guards the third payload field on core.Ref.
// Headers holds a row-list's column order — strings the Ref/Inline walk never
// visits, so a secret echoed as a COLUMN NAME used to survive into durable
// storage and the run-detail UI.
func TestRedactResult_ScrubsHeaders(t *testing.T) {
	set := newSecretSet()
	set.add("sk_live_supersecret")

	shared := []string{"id", "sk_live_supersecret", "amount"}
	result := core.Result{
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"rows": {
				MIME:    "application/json",
				Inline:  []any{map[string]any{"id": 1}},
				Headers: shared,
			},
		},
	}

	redactResult(&result, set)

	blob, _ := json.Marshal(result)
	if strings.Contains(string(blob), "sk_live_supersecret") {
		t.Fatalf("secret survived redaction in Headers: %s", blob)
	}
	got := result.Output["rows"].Headers
	if !reflect.DeepEqual(got, []string{"id", redactionMarker, "amount"}) {
		t.Fatalf("unexpected headers after redaction: %#v", got)
	}
	// The caller's slice must be untouched: a Ref's Headers can share a backing
	// array with a value another reader still holds (a write-dedupe entry).
	if shared[1] != "sk_live_supersecret" {
		t.Fatalf("redaction mutated the caller's slice in place: %#v", shared)
	}
}

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

const covSecret = "topsecretvalue123"

func newCovSecretSet() *secretSet {
	s := newSecretSet()
	s.add(covSecret)
	return s
}

func TestRedactValue_AllShapes_Cov(t *testing.T) {
	set := newCovSecretSet()
	marker := redactionMarker

	tests := []struct {
		name string
		in   any
		want any
	}{
		{name: "nil", in: nil, want: nil},
		{name: "string", in: covSecret, want: marker},
		{name: "bytes", in: []byte(covSecret), want: []byte(marker)},
		{
			name: "map string any redacts key and value",
			in:   map[string]any{covSecret: covSecret, "safe": "ok"},
			want: map[string]any{marker: marker, "safe": "ok"},
		},
		{
			name: "map string string",
			in:   map[string]string{covSecret: covSecret},
			want: map[string]string{marker: marker},
		},
		{
			name: "slice string",
			in:   []string{covSecret, "ok"},
			want: []string{marker, "ok"},
		},
		{
			name: "slice map string any",
			in:   []map[string]any{{"k": covSecret}},
			want: []map[string]any{{"k": marker}},
		},
		{
			name: "slice map string string",
			in:   []map[string]string{{"k": covSecret}},
			want: []map[string]string{{"k": marker}},
		},
		{
			name: "slice any",
			in:   []any{covSecret, 1},
			want: []any{marker, 1},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := redactValue(tt.in, set)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestRedactReflect_Cov(t *testing.T) {
	set := newCovSecretSet()
	marker := redactionMarker

	// Reflective slice (a concrete []int won't match the fast path's []any).
	if got := redactValue([]int{1, 2}, set); !reflect.DeepEqual(got, []any{1, 2}) {
		t.Errorf("reflective int slice = %#v", got)
	}

	// Reflective map with non-string keys -> stringified keys, redacted values.
	gotMap, ok := redactValue(map[int]string{7: covSecret}, set).(map[string]any)
	if !ok {
		t.Fatalf("reflective map type = %T", redactValue(map[int]string{7: covSecret}, set))
	}
	if gotMap["7"] != marker {
		t.Errorf("reflective map[7] = %v, want marker", gotMap["7"])
	}

	// Pointer to a string secret -> dereferenced and redacted.
	s := covSecret
	if got := redactValue(&s, set); got != marker {
		t.Errorf("pointer deref = %v, want marker", got)
	}

	// Nil pointer -> returned unchanged.
	var np *string
	if got := redactValue(np, set); got != any(np) {
		t.Errorf("nil pointer = %#v, want unchanged", got)
	}

	// Scalar default branch (int) -> returned unchanged.
	if got := redactReflect(42, set); got != 42 {
		t.Errorf("scalar = %v, want 42", got)
	}
}

func TestRedactProgressEvent_Cov(t *testing.T) {
	// Empty set: event returned untouched.
	empty := newSecretSet()
	p := core.Progress{Message: covSecret}
	if got := redactProgressEvent(p, empty); got.Message != covSecret {
		t.Errorf("empty set should not redact, got %q", got.Message)
	}

	set := newCovSecretSet()
	in := core.Progress{
		Message: "calling api with " + covSecret,
		Data:    map[string]any{"url": covSecret, "ok": true},
	}
	out := redactProgressEvent(in, set)
	if out.Message != "calling api with "+redactionMarker {
		t.Errorf("message = %q", out.Message)
	}
	if out.Data["url"] != redactionMarker {
		t.Errorf("data.url = %v, want marker", out.Data["url"])
	}
	if out.Data["ok"] != true {
		t.Errorf("data.ok mutated = %v", out.Data["ok"])
	}

	// Nil Data is left alone.
	out = redactProgressEvent(core.Progress{Message: covSecret}, set)
	if out.Data != nil {
		t.Errorf("nil data should stay nil, got %v", out.Data)
	}
}

func TestRedactProgress_NilDst_Cov(t *testing.T) {
	// dst == nil: returns nil channel and an already-closed done.
	ch, done := redactProgress(context.Background(), nil, newCovSecretSet())
	if ch != nil {
		t.Error("nil dst should yield nil channel")
	}
	select {
	case <-done:
	default:
		t.Error("done should be closed immediately for nil dst")
	}
}

func TestRedactProgress_ForwardsRedacted_Cov(t *testing.T) {
	set := newCovSecretSet()
	dst := make(chan core.Progress, 4)

	in, done := redactProgress(context.Background(), dst, set)
	if in == nil {
		t.Fatal("non-nil dst should yield a channel")
	}
	in <- core.Progress{Message: "tok=" + covSecret}
	close(in)
	<-done
	close(dst)

	got := <-dst
	if got.Message != "tok="+redactionMarker {
		t.Errorf("forwarded message = %q", got.Message)
	}
}

func TestRedactProgress_ConsumerGone_Cov(t *testing.T) {
	set := newCovSecretSet()
	// Unbuffered dst with no reader, plus a cancelled ctx, exercises the
	// ctx.Done() drain path so the goroutine never blocks the producer.
	dst := make(chan core.Progress)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	in, done := redactProgress(ctx, dst, set)
	in <- core.Progress{Message: covSecret}
	in <- core.Progress{Message: "another"}
	close(in)
	<-done // must complete without deadlock
}
