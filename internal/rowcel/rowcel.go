// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package rowcel is the shared CEL surface for row-level expressions: the
// environment, the cost ceiling, and compile/eval helpers that every consumer
// of the no-code condition builder agrees on.
//
// The web UI's row-condition builder (SchemaForm, format:"row-condition")
// emits CEL against a `row`/`now` grammar. Several engines run that CEL — the
// transform drops (compute_rows, route_rows, split_rows) and the Collections
// "Find rows" reader. Pinning the environment and cost limit here means the
// builder targets ONE grammar and the engines can't drift apart: change the
// scope or the ceiling in one place and every consumer moves together.
package rowcel

import (
	"fmt"
	"time"

	"github.com/google/cel-go/cel"
)

// CostLimit caps the abstract evaluation cost of a single expression. CEL has
// no wall-clock budget, so without this a pathological expression (deep
// nesting, large string/list ops) over a big row set could burn CPU unbounded
// and ignore job cancellation. The ceiling is far above any ordinary
// field/date expression but stops runaway inputs; an over-budget eval fails the
// row with a cost error rather than hanging the worker.
const CostLimit uint64 = 1_000_000

// Env builds the CEL environment with two variables in scope:
//
//   - row: the current row as map<string, dyn>.
//   - now: the current time as a timestamp, so filters can express "overdue",
//     "last week", "due tomorrow" without a precomputed date column.
//
// extra options are appended for callers that need more (e.g. a computed
// variable). Bound at eval time by Vars.
func Env(extra ...cel.EnvOption) (*cel.Env, error) {
	opts := []cel.EnvOption{
		cel.Variable("row", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("now", cel.TimestampType),
		// Allow int/double/uint to be compared with each other. The no-code
		// condition builder emits integer literals for numeric operators
		// (e.g. `double(row.amount) > 100`), which CEL otherwise rejects as a
		// double-vs-int type error; without this, the builder's own output
		// wouldn't compile.
		cel.CrossTypeNumericComparisons(true),
	}
	return cel.NewEnv(append(opts, extra...)...)
}

// Vars is the activation for one row evaluation. `now` is sampled per call;
// within a batch that's day-granularity stable, which is all the time-window
// filters need.
func Vars(row map[string]any) map[string]any {
	return map[string]any{"row": row, "now": time.Now().UTC()}
}

// Compile compiles expr against env with the shared cost ceiling. label
// prefixes any error message ("filter", "compute column foo") so the failure
// names the field the user was editing.
func Compile(env *cel.Env, expr, label string) (cel.Program, error) {
	ast, issues := env.Compile(expr)
	if issues != nil && issues.Err() != nil {
		return nil, fmt.Errorf("%s: %v", label, issues.Err())
	}
	prog, err := env.Program(ast, cel.CostLimit(CostLimit))
	if err != nil {
		return nil, fmt.Errorf("%s: program: %w", label, err)
	}
	return prog, nil
}

// EvalBool evaluates prog for row and requires a bool result — the contract a
// filter predicate must satisfy. A non-bool result is a user error (the
// expression returned a value, not a condition), reported as such.
func EvalBool(prog cel.Program, row map[string]any) (bool, error) {
	v, _, err := prog.Eval(Vars(row))
	if err != nil {
		return false, err
	}
	b, ok := v.Value().(bool)
	if !ok {
		return false, fmt.Errorf("filter expression must return bool, got %T", v.Value())
	}
	return b, nil
}
