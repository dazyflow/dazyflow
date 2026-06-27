// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"
	"reflect"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

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
