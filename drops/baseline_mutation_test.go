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
// common param at once. That works for drops whose params are largely
// independent, but it systematically under-covers CONNECTOR drops: they
// validate a required param early (a URL, an API key, a channel), reject the
// nasty value there, and return before any of their HTTP-building, response-
// parsing or output-shaping code runs. So the adversarial corpus never
// actually reached the code paths most likely to mishandle it.
//
// This sweep fixes that by starting from a drop's own worked example — a
// VALID baseline, which every drop is required to ship (see
// examples_contract_test.go) — and corrupting exactly one param at a time.
// Every other param stays valid, so validation of those passes and execution
// proceeds deeper into the drop before meeting the hostile value.
//
// It asserts the same system-safety contract as the other sweeps (no panic,
// no hang, Result contract honoured) plus the output-port contract, which is
// how it also covers the drops the port check in output_contract_test.go
// could never reach: that check only asserts on StatusOK runs, and a
// connector never reaches StatusOK under the spray.
//
// Connector drops still won't make a real network call here (no credentials,
// no reachable host), so the deepest layers stay out of reach — but
// param-handling, template rendering and error-shaping now get exercised
// with hostile input instead of being skipped.

// baselineParams returns a drop's first worked example decoded into a params
// map, or nil when it has no usable example.
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

// TestAllDrops_BaselineMutation corrupts one param at a time on an otherwise
// valid job and asserts the safety + output-port contracts hold.
func TestAllDrops_BaselineMutation(t *testing.T) {
	ws := t.TempDir()
	scratch := t.TempDir()

	for _, d := range allDrops(t) {
		d := d
		t.Run(d.id, func(t *testing.T) {
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
					// Fresh copy per run: drops resolve params in place.
					params := make(map[string]any, len(base))
					for k, bv := range base {
						params[k] = bv
					}
					params[key] = v

					job := core.Job{
						ID:            "mutate-job",
						GraphID:       "mutate-graph",
						NodeID:        "mutate-node",
						Tenant:        "mutate-tenant",
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
						// A transport error is allowed — it's a clean failure.
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
					// Output-port contract on a SUCCESSFUL run. This is the
					// coverage the spray couldn't give connector drops.
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
