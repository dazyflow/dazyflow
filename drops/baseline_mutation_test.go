// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package drops_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
)

// The one-value spray in harness_test.go puts the SAME nasty value into every
// common param at once. That works for drops whose params are independent, but
// it systematically under-covers CONNECTOR drops: they validate a required param
// early, reject the nasty value there, and return before any HTTP-building,
// response-parsing or output-shaping code runs.
//
// This sweep starts instead from a drop's own worked example — a VALID baseline
// every drop is required to ship — and corrupts exactly one param at a time, so
// execution proceeds deeper before meeting the hostile value.
//
// It asserts the same system-safety contract as the other sweeps (no panic, no
// hang, Result contract honoured) plus the output-port contract, which is how it
// covers what output_contract_test.go cannot: that check only asserts on
// StatusOK runs, and a connector never reaches StatusOK under the spray.
//
// Connector drops still make no real network call here, so the deepest layers
// stay out of reach — but param handling, template rendering and error shaping
// now get exercised with hostile input.

func baselineParams(m core.Manifest) map[string]any {
	for _, ex := range m.Examples {
		if len(ex.Params) == 0 {
			continue
		}
		var p map[string]any
		if err := json.Unmarshal(ex.Params, &p); err != nil {
			continue
		}
		if len(p) > 0 {
			return p
		}
	}
	return nil
}

// mutationValues is a deliberately small slice of nastyValues(): this sweep
// runs (params × values) per drop rather than (values) per drop, so the full
// corpus would multiply the suite's runtime by the average param count. These
// are the shapes that have historically broken param handling — traversal,
// injection, type confusion, and size.
func mutationValues() []any {
	return []any{
		"",
		"../../../../etc/passwd",
		"'; DROP TABLE jobs; --",
		"${secret.MASTER_KEY}",
		"http://169.254.169.254/latest/meta-data/",
		"\x00\x01\x02",
		strLong(70000),
		-1,
		0,
		1 << 40,
		true,
		nil,
		[]any{1, "two", nil},
		map[string]any{"nested": map[string]any{"deep": []any{"x"}}},
	}
}

func strLong(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'A'
	}
	return string(b)
}

// Corrupts one param at a time on an otherwise valid job and asserts the
// safety + output-port contracts hold.
func TestAllDrops_BaselineMutation(t *testing.T) {
	for _, d := range allDrops(t) {
		d := d
		t.Run(d.id, func(t *testing.T) {
			t.Parallel()
			ws, scratch := t.TempDir(), t.TempDir()
			base := baselineParams(d.manifest)
			if base == nil {
				t.Skipf("drop %q ships no example params to build a baseline from", d.id)
			}

			declared := map[string]bool{core.PassPort: true}
			for _, p := range d.manifest.Outputs {
				declared[p.Port] = true
			}

			for key := range base {
				for vi, v := range mutationValues() {
					params := make(map[string]any, len(base))
					for k, bv := range base {
						params[k] = bv
					}
					params[key] = v

					job := core.Job{
						ID:            "mutate-job-" + d.id,
						GraphID:       "mutate-graph-" + d.id,
						NodeID:        "mutate-node-" + d.id,
						Tenant:        "mutate-tenant-" + d.id,
						Params:        params,
						WorkspaceRoot: ws,
						ScratchRoot:   scratch,
					}
					out := runDropSafely(t.Context(), d.transport, job, 1500*time.Millisecond)

					switch {
					case out.panicVal != nil:
						t.Errorf("panic with %s=<value %d>: %v\n%s", key, vi, out.panicVal, out.stack)
						continue
					case out.timedOut:
						t.Errorf("hang with %s=<value %d>: ignored context", key, vi)
						continue
					case out.err != nil:
						continue
					}
					// Result contract: a FAILED status must carry an error so
					// the caller can report why. StatusAwaiting is not a
					// failure — it parks the node for an external resume
					// (await_approval, subgraph) — so it legitimately carries
					// no error.
					switch out.result.Status {
					case core.StatusOK, core.StatusAwaiting:
					default:
						if out.result.Error == nil {
							t.Errorf("%s=<value %d>: status %q with a nil Error — callers can't report why",
								key, vi, out.result.Status)
						}
					}
					if out.result.Status != core.StatusOK {
						continue
					}
					for port := range out.result.Output {
						if !declared[port] {
							t.Errorf("%s=<value %d>: emitted undeclared output port %q (declared: %v)",
								key, vi, port, keys(declared))
						}
					}
				}
			}
		})
	}
}
