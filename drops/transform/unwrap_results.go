package transform

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/engine"
)

// forEachListMIME mirrors the (unexported) flow.MIMEList constant — the
// wire MIME for_each stamps on its `results` output. Duplicated rather
// than imported to avoid a transform→flow package dependency; it's a
// stable wire constant, asserted by flow's own tests.
const forEachListMIME = "application/x-hazyflow-list+json"

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "unwrap_results",
			Version:     "1.0",
			Label:       "Unwrap results",
			Color:       "#5a9bd4",
			Icon:        "package-open",
			Category:    "transformation",
			Provider:    "internal",
			Tags:        []string{"transform", "for_each", "results", "flatten", "unwrap", "rows"},
			Description: "Flatten a for_each `results` list back into plain rows. Each for_each result is a wrapper ({status, output, error}); the tabular drops downstream want the step's actual output, not the wrapper. unwrap_results pulls one output port out of every result and emits them as rows — so `gmail_search_messages → for_each(gmail_get_message) → unwrap_results → compute_rows` finally lets compute see `row.headers.From` instead of `row.output.message…`. Failed items are skipped by default (the for_each `errors` port still carries them). When the step has one output port, you can leave `port` blank.",
			Summary:     "Pull one output port out of each for_each result and emit them as a flat rows list.",
			Examples: []core.ParamsExample{
				{
					Title:  "Unwrap gmail_get_message results",
					Params: json.RawMessage(`{"port":"message"}`),
					Notes:  "for_each ran gmail_get_message per ID; each result's `message` output becomes one row.",
				},
				{
					Title:  "Single-output step (port inferred)",
					Params: json.RawMessage(`{}`),
					Notes:  "When the step emits exactly one output port, unwrap_results uses it without naming it.",
				},
				{
					Title:  "Include failed items as error rows",
					Params: json.RawMessage(`{"port":"message","skip_errors":false}`),
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "results", Label: "for_each results", Required: true, MIME: []string{forEachListMIME}},
			},
			Outputs: []core.Port{
				{Port: "rows", Label: "Rows", MIME: []string{"application/json"}},
				{Port: "headers", Label: "Headers", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"port": {
						"type":"string",
						"description":"Which output port of the step to extract from each result. Optional when the step has exactly one output port."
					},
					"skip_errors": {
						"type":"boolean",
						"default":true,
						"description":"Drop results whose status is error. When false, failed items emit a row {_error_code, _error_message, _index} instead."
					}
				}
			}`),
			Idempotent: true,
		},
		Execute: executeUnwrapResults,
	})
}

// executeUnwrapResults turns for_each's wrapped results into flat rows.
//
// for_each emits `results` as a list of Result wrappers — each item is
// {status, output:{port: value}, error} — because it can't know what
// downstream wants from a heterogeneous fan-out. Every tabular drop,
// though, expects rows: bare objects whose keys are columns. Without a
// bridge, `for_each(gmail_get_message) → compute_rows` reads
// row.headers.From against the wrapper, not the message, and silently
// produces garbage (the defect that grounded both Gmail templates).
//
// unwrap_results is that bridge. For each result it selects one output
// port (named, or inferred when the step has exactly one) and unwraps
// its Ref to the underlying value. A value that is itself a list of
// objects is flattened (so a step that returns rows contributes many);
// a single object contributes one row; a scalar is wrapped as
// {"value": x} so it still lands as a row.
//
// Failed items are skipped by default — they already surface on the
// for_each `errors` port, and a half-broken row would poison the table.
// Set skip_errors=false to fold them in as explicit error rows instead.
func executeUnwrapResults(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	resultsRef, ok := job.Input["results"]
	if !ok {
		return errResult(job, "missing_input", "input port 'results' is required"), nil
	}
	wrappers, err := normalizeResultList(resultsRef.Inline)
	if err != nil {
		return errResult(job, "bad_input", err.Error()), nil
	}

	port := paramStringOr(job.Params, "port", "")
	skipErrors := true
	if v, ok := job.Params["skip_errors"].(bool); ok {
		skipErrors = v
	}

	out := make([]map[string]any, 0, len(wrappers))
	for i, w := range wrappers {
		payload, ok := asAnyMap(w)
		if !ok {
			return errResult(job, "bad_input",
				fmt.Sprintf("result %d is not a Result wrapper (got %T)", i, w)), nil
		}

		status := fmt.Sprintf("%v", payload["status"])
		if status == core.StatusError {
			if skipErrors {
				continue
			}
			out = append(out, errorRow(i, payload["error"]))
			continue
		}

		outputs, ok := asAnyMap(payload["output"])
		if !ok {
			return errResult(job, "bad_input",
				fmt.Sprintf("result %d has no output map", i)), nil
		}

		chosen, err := selectPort(outputs, port)
		if err != nil {
			return errResult(job, "bad_param", fmt.Sprintf("result %d: %v", i, err)), nil
		}

		value := refInline(chosen)
		out = append(out, valueToRows(value)...)
	}

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"rows":    {MIME: "application/json", Inline: out},
			"headers": {MIME: "application/json", Inline: deriveHeaders(out)},
		},
	}, nil
}

// selectPort returns the chosen output port's Ref. When port is named it
// must exist; when blank, the step must have exactly one output port so
// the choice is unambiguous.
func selectPort(outputs map[string]any, port string) (any, error) {
	if port != "" {
		v, ok := outputs[port]
		if !ok {
			return nil, fmt.Errorf("output port %q not found (available: %s)", port, strings.Join(sortedKeys(outputs), ", "))
		}
		return v, nil
	}
	switch len(outputs) {
	case 0:
		return nil, fmt.Errorf("result has no output ports")
	case 1:
		for _, v := range outputs {
			return v, nil
		}
	}
	return nil, fmt.Errorf("step has %d output ports (%s); set 'port' to pick one", len(outputs), strings.Join(sortedKeys(outputs), ", "))
}

// normalizeResultList coerces the for_each results input into a list of
// wrapper values. In-process it arrives as []core.Ref (each .Inline a
// payload map); after a JSON round-trip it's []any of payload maps, or a
// JSON string. Each returned element is a single wrapper (Ref or map).
func normalizeResultList(inline any) ([]any, error) {
	switch v := inline.(type) {
	case nil:
		return nil, nil
	case []core.Ref:
		out := make([]any, len(v))
		for i, r := range v {
			out[i] = r.Inline
		}
		return out, nil
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = refInline(item)
		}
		return out, nil
	case string:
		var parsed []any
		if err := json.Unmarshal([]byte(v), &parsed); err != nil {
			return nil, fmt.Errorf("results JSON: %w", err)
		}
		return parsed, nil
	}
	return nil, fmt.Errorf("results: expected a for_each results list, got %T", inline)
}

// refInline unwraps a core.Ref (or a JSON-serialized Ref {"data":…}) to
// its underlying value, passing plain values through unchanged.
func refInline(v any) any {
	switch r := v.(type) {
	case core.Ref:
		return r.Inline
	case *core.Ref:
		return r.Inline
	case map[string]any:
		// A serialized Ref carries the value under "data" (Ref.Inline's
		// json tag). Anything else is already a plain value.
		if d, ok := r["data"]; ok {
			if _, hasMIME := r["mime"]; hasMIME {
				return d
			}
		}
		return r
	}
	return v
}

// asAnyMap accepts the two shapes an output/payload map takes: the
// in-process map[string]core.Ref / map[string]any, or anything coerced
// from JSON.
func asAnyMap(v any) (map[string]any, bool) {
	switch m := v.(type) {
	case map[string]any:
		return m, true
	case map[string]core.Ref:
		out := make(map[string]any, len(m))
		for k, r := range m {
			out[k] = r
		}
		return out, true
	}
	return nil, false
}

// valueToRows turns an unwrapped port value into zero or more rows. A
// list of objects flattens to many rows; a single object is one row; a
// scalar (or anything else) is wrapped as {"value": v} so it still lands
// in the table.
func valueToRows(value any) []map[string]any {
	switch v := value.(type) {
	case nil:
		return nil
	case map[string]any:
		return []map[string]any{v}
	case []map[string]any:
		return v
	case []any:
		out := make([]map[string]any, 0, len(v))
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				out = append(out, m)
			} else {
				out = append(out, map[string]any{"value": item})
			}
		}
		return out
	default:
		return []map[string]any{{"value": v}}
	}
}

func errorRow(index int, errPayload any) map[string]any {
	row := map[string]any{"_index": index}
	if m, ok := errPayload.(map[string]any); ok {
		row["_error_code"] = m["code"]
		row["_error_message"] = m["message"]
	} else if errPayload != nil {
		row["_error_message"] = fmt.Sprintf("%v", errPayload)
	}
	return row
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
