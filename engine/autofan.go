// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"
	"fmt"
	"reflect"

	"git.sr.ht/~klahr/dazyflow/core"
)

// maxAutoFanItems caps how many items a single node will be auto-run over
// before the engine refuses. It's the backend half of the many→one guardrail:
// past this, the flow author should be explicit (the editor's "run for each?"
// confirmation — a frontend concern — covers smaller-but-still-large lists).
const maxAutoFanItems = 1000

// detectAutoFan decides whether a node should run once PER ITEM instead of once.
// This is the many→one rule of the simplified data model: when a single-value
// ("one") input port receives a LIST, the step is meant to run for each item.
//
// It fires only on an UNAMBIGUOUS mismatch: exactly one input port that is
// non-variadic, declared single (Cardinality One), and typed (Kind != Any —
// an untyped/pass-through port legitimately takes the whole list) received a
// list value at runtime. Zero such ports → run normally; more than one →
// ambiguous, so don't auto-fan (the many_into_one lint already flagged it).
//
// Returns the port to iterate, the items, and ok.
func detectAutoFan(manifest core.Manifest, input map[string]core.Ref) (fanPort string, items []any, ok bool) {
	// Flow-control drops (routers/predicates like Branch — NoPassthrough) must
	// never auto-fan: "route this payload" is a single decision, not a per-item
	// loop. A list arriving on such a drop's typed scalar input (e.g. Branch's
	// bool `condition`) would otherwise silently turn the router into an
	// aggregating loop. They take the whole value as-is.
	if manifest.NoPassthrough {
		return "", nil, false
	}
	found := 0
	for _, p := range manifest.Inputs {
		if p.Variadic || p.Cardinality() != core.One || p.Kind() == core.KindAny {
			continue
		}
		ref, present := input[p.Port]
		if !present || ref.Inline == nil {
			continue
		}
		list, isList := asList(ref.Inline)
		if !isList {
			continue
		}
		found++
		fanPort, items = p.Port, list
	}
	if found != 1 {
		return "", nil, false
	}
	return fanPort, items, true
}

// asList reports whether v is a slice/array and returns its elements as []any.
// Strings (and []byte, which is a slice but a scalar blob) are NOT lists.
func asList(v any) ([]any, bool) {
	if v == nil {
		return nil, false
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
		return nil, false
	}
	if rv.Type().Elem().Kind() == reflect.Uint8 { // []byte = blob, not a list
		return nil, false
	}
	out := make([]any, rv.Len())
	for i := range out {
		out[i] = rv.Index(i).Interface()
	}
	return out, true
}

// runMaybeFanned runs a node's transport once, OR — when a single-value input
// received a list (detectAutoFan) — once per item, aggregating each output port
// into a list so the node's result becomes "many". This is what makes
// "5 new signups → notify" notify five times without a manual For-each.
//
// exec is the caller's single-execution strategy (it wires progress); calling
// it per item reuses that path unchanged. Fail-fast: the first item that errors
// aborts the whole node with that error (matching the batch semantics of the
// row drops). The per-item input is a shallow copy with only the fanned port
// replaced, so params/other inputs stay constant across items.
func runMaybeFanned(
	ctx context.Context,
	manifest core.Manifest,
	job core.Job,
	secrets *secretSet,
	transport core.Transport,
	exec func(ctx context.Context, transport core.Transport, job core.Job, secrets *secretSet) (core.Result, error),
) (core.Result, error) {
	fanPort, items, fan := detectAutoFan(manifest, job.Input)
	if !fan {
		return exec(ctx, transport, job, secrets)
	}
	if len(items) > maxAutoFanItems {
		return core.Result{
			Status: core.StatusError,
			Error: &core.JobError{
				Code: "too_many_items",
				Message: fmt.Sprintf("step %q would run %d times (input %q got %d items, limit %d) — "+
					"reduce the list or wire it through a step that handles the whole list",
					manifest.ID, len(items), fanPort, len(items), maxAutoFanItems),
			},
		}, nil
	}

	// Zero items: the node runs zero times, so the aggregation loop below observes
	// no output ports and would return a Result with NO outputs at all — the
	// declared ports vanish instead of reading as "zero items". Downstream that
	// misleads both execution paths: an edge from an absent port classifies as
	// dormant (classifyEdge), skipping the rest of the branch, and on the
	// in-process path assembleInput simply omits the input, so a loop-body node
	// can run with a required input unwired. Emit each declared output port as an
	// empty list instead — "many" with zero items, the shape for_each already
	// returns for an empty input.
	if len(items) == 0 {
		return core.Result{Status: core.StatusOK, Output: emptyFanOutputs(manifest)}, nil
	}

	baseMIME := job.Input[fanPort].MIME
	agg := map[string][]any{}
	// headers carries a row-list output's column order (Ref.Headers) onto the
	// aggregate. Every item of the SAME drop emits the same order, so the first
	// non-empty one wins. Without this a fanned parse_csv / regex /
	// group_aggregate / sheets_read_range loses the column order its value is
	// supposed to carry with it.
	headers := map[string][]string{}
	order := []string{} // preserve first-seen output-port order for deterministic results
	for _, item := range items {
		perItem := make(map[string]core.Ref, len(job.Input))
		for k, v := range job.Input {
			perItem[k] = v
		}
		perItem[fanPort] = core.Ref{MIME: baseMIME, Inline: item}
		itemJob := job
		itemJob.Input = perItem

		res, err := exec(ctx, transport, itemJob, secrets)
		if err != nil {
			return res, err
		}
		if res.Status == core.StatusError {
			return res, nil // fail-fast on the first item error
		}
		for port, ref := range res.Output {
			if _, seen := agg[port]; !seen {
				order = append(order, port)
			}
			agg[port] = append(agg[port], fannedValue(ref))
			if len(headers[port]) == 0 && len(ref.Headers) > 0 {
				headers[port] = ref.Headers
			}
		}
	}

	out := make(map[string]core.Ref, len(order))
	for _, port := range order {
		out[port] = core.Ref{MIME: "application/json", Inline: agg[port], Headers: headers[port]}
	}
	return core.Result{Status: core.StatusOK, Output: out}, nil
}

// fannedValue is the value the aggregate carries for one item's output ref.
//
// Inline is the common case. An OUT-OF-LINE ref carries its payload in Ref with
// Inline nil — that's every file-producing drop (http_download, file_write,
// drive_download, excel_write, sheets_export_pdf), so fall back to the ref and
// let the aggregate become a list of workspace paths. That matches the
// path-as-value convention git_checkout already emits (it sets Ref and Inline to
// the same path). Reading Inline alone aggregated a fanned file-producing node
// to [nil, nil, …] while still reporting ok — silent data loss.
func fannedValue(ref core.Ref) any {
	if ref.Inline != nil {
		return ref.Inline
	}
	if ref.Ref != "" {
		return ref.Ref
	}
	return nil
}

// emptyFanOutputs is a fanned node's output shape for zero items: every declared
// output port carrying an empty list.
//
// PassPort is deliberately excluded. core.ApplyPassthrough threads the node's
// pass INPUT onto that output after runMaybeFanned returns, and only when the
// port isn't already set — so seeding it here would replace the threaded value
// with an empty list. The manifest reaching this point hasn't been through
// core.WithPassthrough (which is what adds the pin), so the port isn't in Outputs
// today; the guard keeps that from becoming a silent trap if it ever is.
func emptyFanOutputs(manifest core.Manifest) map[string]core.Ref {
	out := make(map[string]core.Ref, len(manifest.Outputs))
	for _, p := range manifest.Outputs {
		if p.Port == core.PassPort {
			continue
		}
		out[p.Port] = core.Ref{MIME: "application/json", Inline: []any{}}
	}
	return out
}
