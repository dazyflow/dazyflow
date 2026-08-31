// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package drops_test

import (
	"strings"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
)

// The output-shape contract: a flow author wires DOWNSTREAM steps onto a
// drop's declared output ports, so those ports are an API. A drop that
// declares a malformed port (empty id, duplicate, no MIME) or — worse —
// EMITS an output port it never declared is schema drift that breaks the
// editor's wiring and any flow built on it. examples_contract_test.go covers
// the INPUT/params side; this is the OUTPUT side, for every built-in at once.

// TestAllDrops_PortsWellFormed asserts the static port declarations are sane:
// unique non-empty ids per side, a human Label, and at least one non-empty
// MIME on every port (the editor types pins by MIME — an empty one renders as
// an untyped wildcard, which for a concrete connector port is a bug).
func TestAllDrops_PortsWellFormed(t *testing.T) {
	for _, d := range allDrops(t) {
		t.Run(d.id, func(t *testing.T) {
			checkPorts(t, "input", d.manifest.Inputs)
			checkPorts(t, "output", d.manifest.Outputs)
		})
	}
}

func checkPorts(t *testing.T, side string, ports []core.Port) {
	t.Helper()
	seen := map[string]bool{}
	for i, p := range ports {
		if strings.TrimSpace(p.Port) == "" {
			t.Errorf("%s port #%d: empty Port id", side, i)
			continue
		}
		if seen[p.Port] {
			t.Errorf("%s port %q: declared more than once", side, p.Port)
		}
		seen[p.Port] = true
		if strings.TrimSpace(p.Label) == "" {
			t.Errorf("%s port %q: empty Label (the editor renders it on the pin)", side, p.Port)
		}
		for j, m := range p.MIME {
			if strings.TrimSpace(m) == "" {
				t.Errorf("%s port %q: MIME entry #%d is empty", side, p.Port, j)
			}
		}
	}
}

// TestAllDrops_EmittedOutputsAreDeclared runs every drop against the
// adversarial corpus and, whenever one returns a successful Result, asserts
// every output port it emitted is declared in the manifest. A drop that emits
// an undeclared port is the output-side twin of the params drift the examples
// contract catches — a downstream step could never have been wired to it, so
// it's silently lost. (Most adversarial inputs make a drop error, which is
// fine: this asserts only on the StatusOK runs, where the contract bites.)
func TestAllDrops_EmittedOutputsAreDeclared(t *testing.T) {
	ws := t.TempDir()
	scratch := t.TempDir()
	for _, d := range allDrops(t) {
		t.Run(d.id, func(t *testing.T) {
			declared := map[string]bool{}
			for _, p := range d.manifest.Outputs {
				declared[p.Port] = true
			}
			// The universal passthrough pin is a reserved convention: the
			// engine prepends it (WithPassthrough) for opted-in drops, and a
			// few (delay) hand-roll emitting on it while opting the auto-pin
			// out. Always allowed.
			declared[core.PassPort] = true
			// NOTE: "meta" is NOT exempt. 31 drops used to emit it without
			// declaring it, which this check never caught because it only
			// asserts on StatusOK runs and a connector never reaches StatusOK
			// under the one-value spray. They now declare it like the six that
			// always did — see baseline_mutation_test.go, which corrupts one
			// param of a VALID example at a time and does reach those paths.
			for _, v := range nastyValues() {
				job := jobWithValue(v, ws, scratch)
				out := runDropSafely(t.Context(), d.transport, job, 1500*time.Millisecond)
				if out.timedOut || out.panicVal != nil || out.err != nil {
					continue
				}
				if out.result.Status != core.StatusOK {
					continue
				}
				for port := range out.result.Output {
					if !declared[port] {
						t.Errorf("emitted undeclared output port %q (declared: %v)", port, keys(declared))
					}
				}
			}
		})
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
