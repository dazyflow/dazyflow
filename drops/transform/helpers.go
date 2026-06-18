// Package transform hosts data-shaping drops — nodes that don't talk
// to anything external, they just rearrange rows that flow between
// other drops. The first inhabitant is map_rows; future siblings
// might include things like sort_rows, dedupe_rows, etc.
package transform

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/google/cel-go/cel"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/drops/internal/limits"
)

// capRows rejects an input list that exceeds the per-drop row ceiling, so a
// transform can't be made to hold (or amplify) an unbounded amount of data in
// memory. Checked before the list is materialized/copied so the oversized
// input is refused, not first allocated.
func capRows(n int) error {
	if max := limits.MaxRows(); n > max {
		return fmt.Errorf("input has %d rows, exceeds the %d-row limit (raise HAZYFLOW_MAX_ROWS to process larger batches)", n, max)
	}
	return nil
}

// newRowCELEnv builds the CEL environment shared by the filtering and
// computing drops (compute_rows, route_rows, split_rows). Two variables
// are in scope:
//
//   - row: the current row as map<string, dyn>.
//   - now: the current time as a timestamp, so filters and computed
//     columns can express "overdue", "last week", "due tomorrow" without
//     the caller precomputing a date column. Bound at eval by celVars.
func newRowCELEnv(extra ...cel.EnvOption) (*cel.Env, error) {
	opts := []cel.EnvOption{
		cel.Variable("row", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("now", cel.TimestampType),
	}
	return cel.NewEnv(append(opts, extra...)...)
}

// celVars is the activation for one row evaluation. `now` is sampled
// per call; within a batch that's day-granularity stable, which is all
// the time-window filters need.
func celVars(row map[string]any) map[string]any {
	return map[string]any{"row": row, "now": time.Now().UTC()}
}

// celCostLimit caps the abstract evaluation cost of a single expression.
// CEL has no wall-clock budget, so without this a pathological expression
// (deep nesting, large string/list ops) over the row ceiling could burn
// CPU unbounded and ignore job cancellation. The ceiling is far above any
// ordinary field/date expression but stops runaway inputs; an over-budget
// eval fails the row with a cost error rather than hanging the worker.
const celCostLimit uint64 = 1_000_000

// celProgram compiles ast into a program with the shared cost ceiling.
// All transform drops go through it so the bound can't be forgotten at a
// single call site.
func celProgram(env *cel.Env, ast *cel.Ast) (cel.Program, error) {
	return env.Program(ast, cel.CostLimit(celCostLimit))
}

func errResult(job core.Job, code, msg string) core.Result {
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusError,
		Error:  &core.JobError{Code: code, Message: msg},
	}
}

// normalizeRows / normalizeHeaders / deriveHeaders mirror the same
// helpers in drops/db. We duplicate rather than import to
// avoid cross-integration coupling — each integration package owns
// its own row-handling so a refactor in one doesn't ripple.
func normalizeRows(inline any) ([]map[string]any, error) {
	if inline == nil {
		return nil, nil
	}
	switch v := inline.(type) {
	case []map[string]any:
		if err := capRows(len(v)); err != nil {
			return nil, err
		}
		return v, nil
	case []map[string]string:
		if err := capRows(len(v)); err != nil {
			return nil, err
		}
		out := make([]map[string]any, len(v))
		for i, r := range v {
			m := make(map[string]any, len(r))
			for k, val := range r {
				m[k] = val
			}
			out[i] = m
		}
		return out, nil
	case []any:
		if err := capRows(len(v)); err != nil {
			return nil, err
		}
		out := make([]map[string]any, 0, len(v))
		for i, item := range v {
			m, err := coerceRowMap(item)
			if err != nil {
				return nil, fmt.Errorf("row %d: %w", i, err)
			}
			out = append(out, m)
		}
		return out, nil
	case map[string]any:
		// A single object is one row. This is the shape a webhook or
		// hosted-form trigger emits for a JSON object body
		// (buildWebhookSeed / collectFormValues), so wiring
		// webhook_input.body straight into a transform's rows port — the
		// most common starter shape — just works instead of failing with
		// "unsupported input type".
		return []map[string]any{v}, nil
	case map[string]string:
		m := make(map[string]any, len(v))
		for k, val := range v {
			m[k] = val
		}
		return []map[string]any{m}, nil
	case string:
		// An empty string is "no rows" (a webhook fired with no body),
		// matching the db drops' contract. Otherwise parse JSON, accepting
		// either an array of objects or a single object.
		if v == "" {
			return nil, nil
		}
		var parsed any
		if err := json.Unmarshal([]byte(v), &parsed); err != nil {
			return nil, fmt.Errorf("rows JSON: %w", err)
		}
		return normalizeRows(parsed)
	}
	return nil, fmt.Errorf("rows: unsupported input type %T", inline)
}

func coerceRowMap(item any) (map[string]any, error) {
	switch m := item.(type) {
	case map[string]any:
		return m, nil
	case map[string]string:
		out := make(map[string]any, len(m))
		for k, v := range m {
			out[k] = v
		}
		return out, nil
	}
	return nil, fmt.Errorf("expected object, got %T", item)
}

func normalizeHeaders(inline any) ([]string, error) {
	switch v := inline.(type) {
	case []string:
		return v, nil
	case []any:
		out := make([]string, len(v))
		for i, h := range v {
			s, ok := h.(string)
			if !ok {
				return nil, fmt.Errorf("headers[%d]: expected string, got %T", i, h)
			}
			out[i] = s
		}
		return out, nil
	}
	return nil, fmt.Errorf("headers: unsupported input type %T", inline)
}

func deriveHeaders(rows []map[string]any) []string {
	seen := map[string]struct{}{}
	for _, r := range rows {
		for k := range r {
			seen[k] = struct{}{}
		}
	}
	headers := make([]string, 0, len(seen))
	for k := range seen {
		headers = append(headers, k)
	}
	sort.Strings(headers)
	return headers
}

// normalizeStringSlice accepts []string or []any-of-string. Used for
// `select` and `drop` params.
func normalizeStringSlice(v any, name string) ([]string, error) {
	switch s := v.(type) {
	case []string:
		return s, nil
	case []any:
		out := make([]string, len(s))
		for i, item := range s {
			str, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("%s[%d]: expected string, got %T", name, i, item)
			}
			out[i] = str
		}
		return out, nil
	}
	return nil, fmt.Errorf("%s: expected array of strings, got %T", name, v)
}

// normalizeStringMap accepts map[string]string or map[string]any-of-string.
// Used for `rename`.
func normalizeStringMap(v any, name string) (map[string]string, error) {
	switch m := v.(type) {
	case map[string]string:
		return m, nil
	case map[string]any:
		out := make(map[string]string, len(m))
		for k, val := range m {
			s, ok := val.(string)
			if !ok {
				return nil, fmt.Errorf("%s[%q]: expected string, got %T", name, k, val)
			}
			out[k] = s
		}
		return out, nil
	}
	return nil, fmt.Errorf("%s: expected object, got %T", name, v)
}

// normalizeAnyMap accepts map[string]any directly or coerces from
// map[string]string. Used for `default`, `filter_eq`, `filter_neq`
// where values can be any JSON type.
func normalizeAnyMap(v any, name string) (map[string]any, error) {
	switch m := v.(type) {
	case map[string]any:
		return m, nil
	case map[string]string:
		out := make(map[string]any, len(m))
		for k, val := range m {
			out[k] = val
		}
		return out, nil
	}
	return nil, fmt.Errorf("%s: expected object, got %T", name, v)
}

// normalizeAnyArrayMap accepts map[string][]any. Used for filter_in
// where each value is a list of allowed values.
func normalizeAnyArrayMap(v any, name string) (map[string][]any, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s: expected object, got %T", name, v)
	}
	out := make(map[string][]any, len(m))
	for k, val := range m {
		arr, ok := val.([]any)
		if !ok {
			// Be lenient: also accept []string from typed callers.
			if ss, ok := val.([]string); ok {
				asAny := make([]any, len(ss))
				for i, s := range ss {
					asAny[i] = s
				}
				out[k] = asAny
				continue
			}
			return nil, fmt.Errorf("%s[%q]: expected array, got %T", name, k, val)
		}
		out[k] = arr
	}
	return out, nil
}
