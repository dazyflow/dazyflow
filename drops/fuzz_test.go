// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package drops_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
)

// FuzzAllDrops feeds the Go fuzzer's bytes — decoded as a params object — to
// a fuzzer-chosen drop, spread across both params and the common input ports.
// The contract is the same as the adversarial sweep: never panic, never hang.
// Crash inputs land under testdata/fuzz/FuzzAllDrops and become permanent
// regression cases. Run with: go test ./drops -run x -fuzz FuzzAllDrops
func FuzzAllDrops(f *testing.F) {
	workspace := f.TempDir()
	scratch := f.TempDir()
	drops := allDrops(f)

	seeds := [][2]any{
		{0, `{}`},
		{1, `{"path":"../../../../etc/passwd"}`},
		{2, `{"path":"scratch://../../etc/passwd"}`},
		{3, `{"url":"http://169.254.169.254/latest/meta-data/","timeout_ms":200}`},
		{4, `{"sql":"SELECT 1","dsn":"postgres://u:p@192.0.2.1:5432/d","params":[1,2]}`},
		{5, `{"table":"x\";DROP TABLE y;--","rows":[{"a":1}]}`},
		{6, `{"template":"row.a + row.b","rows":[{"a":1,"b":2}]}`},
		{7, `{"ms":-1,"timeout_ms":0}`},
		{8, `{"in":"[1,2,[3,[4,[5]]]]"}`},
		{9, `{"command":"echo","args":["hi",";rm -rf /"]}`},
	}
	for _, s := range seeds {
		f.Add(s[0].(int), []byte(s[1].(string)))
	}

	f.Fuzz(func(t *testing.T, idx int, paramsJSON []byte) {
		d := drops[((idx%len(drops))+len(drops))%len(drops)]

		// Best-effort decode; malformed JSON yields a nil params map, which
		// is itself a valid (and adversarial) input.
		var params map[string]any
		_ = json.Unmarshal(paramsJSON, &params)

		input := make(map[string]core.Ref, len(commonInputPorts))
		for _, p := range commonInputPorts {
			if params != nil {
				if v, ok := params[p]; ok {
					input[p] = core.Ref{Inline: v}
					continue
				}
			}
			input[p] = core.Ref{Inline: params} // spray the whole blob too
		}

		job := core.Job{
			ID:            "fuzz",
			GraphID:       "g",
			NodeID:        "n",
			Tenant:        "t",
			Params:        params,
			Input:         input,
			WorkspaceRoot: workspace,
			ScratchRoot:   scratch,
		}

		out := runDropSafely(context.Background(), d.transport, job, 1500*time.Millisecond)
		if out.panicVal != nil {
			t.Fatalf("drop %q PANIC: %v\n%s", d.id, out.panicVal, out.stack)
		}
		if out.timedOut {
			t.Fatalf("drop %q HANG — Execute ignored context", d.id)
		}
		assertResultContract(t, "drop "+d.id, out)
	})
}
