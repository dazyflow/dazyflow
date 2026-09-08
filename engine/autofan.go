// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"
	"fmt"
	"reflect"

	"github.com/dazyflow/dazyflow/core"
)

const maxAutoFanItems = 1000

func detectAutoFan(manifest core.Manifest, input map[string]core.Ref) (fanPort string, items []any, ok bool) {
	// Routers and predicates must never auto-fan: "route this payload" is a single
	// decision, not a per-item loop, and a list on Branch's bool `condition` would
	// otherwise silently turn the router into an aggregating loop.
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

// asList: strings, and []byte, are NOT lists.
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

	// With zero items the aggregation loop sees no output ports, so the declared
	// ports would vanish rather than read as "zero items" — and downstream, an edge
	// from an absent port classifies as dormant, skipping the branch, while
	// AssembleInput omits the input so a loop-body node can run with a required input
	// unwired. Emit each declared port as an empty list instead.
	if len(items) == 0 {
		return core.Result{Status: core.StatusOK, Output: emptyFanOutputs(manifest)}, nil
	}

	baseMIME := job.Input[fanPort].MIME
	agg := map[string][]any{}
	// headers carries a row-list output's column order onto the aggregate. Every
	// item of the SAME drop emits the same order, so the first non-empty one wins.
	// Without it a fanned parse_csv or sheets_read_range loses the column order its
	// value is supposed to carry.
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

func fannedValue(ref core.Ref) any {
	if ref.Inline != nil {
		return ref.Inline
	}
	if ref.Ref != "" {
		return ref.Ref
	}
	return nil
}

// emptyFanOutputs deliberately excludes PassPort: core.ApplyPassthrough threads
// the node's pass INPUT onto that output afterwards, and only when unset, so
// seeding it here would replace the threaded value with an empty list.
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
