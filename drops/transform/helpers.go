// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package transform hosts data-shaping drops — nodes that don't talk
// to anything external, they just rearrange the rows that flow between
// other drops: map_rows, compute_rows, route_rows, split_rows,
// sort_rows, dedupe_rows, group_aggregate, join_rows, render_text, and
// the JSON/results parsers. They all share the {column: value}[] row
// shape emitted by excel_read and the db query drops, normalized
// through drops/internal/rows.
package transform

import (
	"fmt"
	"strings"

	"github.com/google/cel-go/cel"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/limits"
	"github.com/dazyflow/dazyflow/drops/internal/rows"
	"github.com/dazyflow/dazyflow/internal/rowcel"
)

// capRows rejects an input list that exceeds the per-drop row ceiling, so a
// transform can't be made to hold (or amplify) an unbounded amount of data in
// memory. Checked before the list is materialized/copied so the oversized
// input is refused, not first allocated.
func capRows(n int) error {
	if max := limits.MaxRows(); n > max {
		return fmt.Errorf("input has %d rows, exceeds the %d-row limit (raise DAZYFLOW_MAX_ROWS to process larger batches)", n, max)
	}
	return nil
}

// newRowCELEnv builds the row/now CEL environment shared by the filtering and
// computing drops (compute_rows, route_rows, split_rows). The environment, the
// cost ceiling, and the compile helper all live in internal/rowcel so
// the no-code condition builder and every engine that runs its CEL stay in
// lockstep — see compileRowExpr / celVars below, which delegate there.
func newRowCELEnv(extra ...cel.EnvOption) (*cel.Env, error) {
	return rowcel.Env(extra...)
}

// celVars is the activation for one row evaluation (delegates to rowcel.Vars).
func celVars(row map[string]any) map[string]any {
	return rowcel.Vars(row)
}

// celProgram compiles ast with the shared cost ceiling (rowcel.CostLimit). All
// transform drops go through it so the bound can't be forgotten at a call site.
func celProgram(env *cel.Env, ast *cel.Ast) (cel.Program, error) {
	return env.Program(ast, cel.CostLimit(rowcel.CostLimit))
}

func errResult(job core.Job, code, msg string) core.Result {
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusError,
		Error:  &core.JobError{Code: code, Message: msg},
	}
}

// normalizeRows / coerceRowMap / normalizeHeaders / deriveHeaders are
// thin aliases over the shared drops/internal/rows package. The
// transform variant caps the input against the row ceiling (so a
// transform can't be made to hold an unbounded list) and accepts a
// single object as a one-row list (the shape a webhook/form body
// arrives in) — both expressed via Options.
func normalizeRows(inline any) ([]map[string]any, error) {
	return rows.Normalize(inline, rows.Options{Cap: capRows, AllowSingleObject: true})
}

func coerceRowMap(item any) (map[string]any, error) {
	return rows.CoerceRowMap(item)
}

func normalizeHeaders(inline any) ([]string, error) {
	return rows.NormalizeHeaders(inline)
}

func deriveHeaders(r []map[string]any) []string {
	return rows.DeriveHeaders(r)
}

// loadRowsAndHeaders is the shared prologue for the row-shaping drops:
// read the required `rows` input and normalize it, then read the
// optional `headers` input (deriving from the rows when none is wired).
// On a bad input it returns a fully-formed error Result and ok=false,
// which callers return verbatim. This collapses the ~15-line read /
// normalize / derive block every transform drop opened with.
func loadRowsAndHeaders(job core.Job) (rowsOut []map[string]any, headers []string, errRes core.Result, ok bool) {
	rowsRef, present := job.Input["rows"]
	if !present {
		return nil, nil, errResult(job, "missing_input", "input port 'rows' is required"), false
	}
	rowsOut, err := normalizeRows(rowsRef.Inline)
	if err != nil {
		return nil, nil, errResult(job, "bad_input", err.Error()), false
	}
	// Folded-headers model: the column order travels ON the rows value
	// (rowsRef.Headers), so there's one wire, not parallel rows + headers
	// ports. Fall back to a legacy separate `headers` input (for graphs/drops
	// not yet migrated), then derive from the row keys.
	switch {
	case len(rowsRef.Headers) > 0:
		headers = rowsRef.Headers
	default:
		if h, ok := job.Input["headers"]; ok && h.Inline != nil {
			headers, err = normalizeHeaders(h.Inline)
			if err != nil {
				return nil, nil, errResult(job, "bad_input", err.Error()), false
			}
		}
	}
	if headers == nil {
		headers = deriveHeaders(rowsOut)
	}
	return rowsOut, headers, core.Result{}, true
}

// resultRows builds the common OK Result that emits a `rows` list plus a
// `headers` list — the epilogue shared by sort_rows, dedupe_rows, and
// the rest of the row-passthrough drops. Drops with extra output ports
// (route_rows' per-slot buckets, dedupe_rows' dropped count) build their
// Result inline instead.
func resultRows(job core.Job, rowsOut []map[string]any, headers []string) core.Result {
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			// One Items port: the column order rides on the rows Ref (Headers).
			// The former parallel `headers` output port is gone.
			"rows": {MIME: "application/json", Inline: rowsOut, Headers: headers},
		},
	}
}

// compileRowExpr compiles a CEL expression against env with the shared
// cost ceiling, wrapping both the compile-time issues and the
// program-build error with label so callers don't repeat the
// env.Compile → issues.Err() → celProgram dance. label is the human
// prefix for the error (e.g. `compute["total"]`, `filter`).
func compileRowExpr(env *cel.Env, expr, label string) (cel.Program, error) {
	ast, issues := env.Compile(expr)
	if issues != nil && issues.Err() != nil {
		return nil, fmt.Errorf("%s: %v", label, issues.Err())
	}
	prog, err := celProgram(env, ast)
	if err != nil {
		return nil, fmt.Errorf("%s: program: %w", label, err)
	}
	return prog, nil
}

// keyString canonicalizes a row's values for the listed columns into one
// hash-table-friendly string, used by dedupe_rows and join_rows to bucket
// rows by identity. Equality uses fmt.Sprint so int 30 and string "30"
// match — the same lenient rule map_rows.filter_eq uses, so users don't
// pre-cast columns coming out of Excel/JSON. Cells are separated by the
// ASCII unit separator (\x1f) so adjacent values can't collide.
func keyString(row map[string]any, cols []string) string {
	parts := make([]string, len(cols))
	for i, c := range cols {
		parts[i] = fmt.Sprint(row[c])
	}
	return strings.Join(parts, "\x1f")
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
