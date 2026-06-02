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
)

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
		return v, nil
	case []map[string]string:
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
		out := make([]map[string]any, 0, len(v))
		for i, item := range v {
			m, err := coerceRowMap(item)
			if err != nil {
				return nil, fmt.Errorf("row %d: %w", i, err)
			}
			out = append(out, m)
		}
		return out, nil
	case string:
		var parsed []map[string]any
		if err := json.Unmarshal([]byte(v), &parsed); err != nil {
			return nil, fmt.Errorf("rows JSON: %w", err)
		}
		return parsed, nil
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
