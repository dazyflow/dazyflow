// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package slack

import (
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

// TestResolveBlocks covers the shapes a user can paste/build into the Blocks
// input: the canonical array, the Block Kit Builder's wrapped {"blocks":[…]}
// payload, a lone block object, and JSON text of each — plus the genuine
// mistakes (malformed JSON, a scalar) that must still error.
func TestResolveBlocks(t *testing.T) {
	block := map[string]any{"type": "divider"}
	arr := []any{block}

	cases := []struct {
		name    string
		inline  any
		wantLen int // expected length of the returned []any
		wantErr bool
	}{
		{"bare array", arr, 1, false},
		{"wrapped payload", map[string]any{"blocks": arr}, 1, false},
		{"lone block object", block, 1, false},
		{"json array string", `[{"type":"divider"}]`, 1, false},
		{"json wrapped string", `{"blocks":[{"type":"divider"},{"type":"section"}]}`, 2, false},
		{"json single object string", `{"type":"divider"}`, 1, false},
		{"json bytes", []byte(`[{"type":"divider"}]`), 1, false},
		{"invalid json string", `not json`, 0, true},
		{"scalar", 42, 0, true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			job := core.Job{Input: map[string]core.Ref{"blocks": {Inline: c.inline}}}
			got, jerr := resolveBlocks(job)
			if c.wantErr {
				if jerr == nil {
					t.Fatalf("expected a JobError, got value %v", got)
				}
				return
			}
			if jerr != nil {
				t.Fatalf("unexpected JobError: %+v", jerr)
			}
			out, ok := got.([]any)
			if !ok {
				t.Fatalf("expected []any, got %T", got)
			}
			if len(out) != c.wantLen {
				t.Errorf("len = %d, want %d (%v)", len(out), c.wantLen, out)
			}
		})
	}
}

// TestResolveBlocks_Absent: no blocks input and no param → (nil, nil) so the
// caller falls through to text-only.
func TestResolveBlocks_Absent(t *testing.T) {
	got, jerr := resolveBlocks(core.Job{})
	if got != nil || jerr != nil {
		t.Fatalf("expected (nil, nil), got (%v, %+v)", got, jerr)
	}
}
