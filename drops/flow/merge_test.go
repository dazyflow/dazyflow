// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package flow

import (
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

func TestMerge_CollectsVariadicInputs(t *testing.T) {
	job := core.Job{
		Input: map[string]core.Ref{
			core.VariadicInputKey("items", 0): {Ref: "a"},
			core.VariadicInputKey("items", 1): {Ref: "b"},
			core.VariadicInputKey("items", 2): {Ref: "c"},
		},
	}
	res, err := executeMerge(t.Context(), job, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	out := res.Output["out"]
	if out.MIME != MIMEList {
		t.Errorf("MIME = %q, want %q", out.MIME, MIMEList)
	}
	refs, ok := out.Inline.([]core.Ref)
	if !ok {
		t.Fatalf("Inline is %T, want []core.Ref", out.Inline)
	}
	if len(refs) != 3 {
		t.Fatalf("got %d refs, want 3", len(refs))
	}
	for i, want := range []string{"a", "b", "c"} {
		if refs[i].Ref != want {
			t.Errorf("refs[%d].Ref = %q, want %q", i, refs[i].Ref, want)
		}
	}
}
