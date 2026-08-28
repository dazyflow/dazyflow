// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package celexpr is the single source of truth for the Expression drop's CEL
// environment. The drop (drops/transform/expression.go) compiles and runs
// formulas with it, and the editor's linter (POST /tools/expression/validate)
// checks them with the SAME env — so a formula the linter accepts is exactly
// one the drop will compile, and vice versa. Kept in its own leaf package (no
// engine/drops imports) so the daemon can validate without pulling in — and
// re-registering — every drop.
package celexpr

import (
	"strings"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/ext"
)

// MaxExpressionLen caps the source length of a formula we are willing to
// parse. cel-go's eval side is bounded by rowcel.CostLimit, but COMPILE is
// not: parse + type-check cost grows with nesting depth and term count, and
// POST /tools/expression/validate compiles whatever the caller sends. A
// deeply nested expression is cheap to transmit and expensive to type-check,
// so without a cap one authenticated request can burn CPU out of proportion
// to its size. 4 KiB is far past any hand-written formula — the editor's
// field is a single line — while cutting the pathological cases off early.
const MaxExpressionLen = 4 << 10

// ErrExpressionTooLong is returned by Validate (and the drop) when a formula
// exceeds MaxExpressionLen. Surfaced as an Issue rather than an error so the
// editor renders it next to the field like any other compile problem.
const errExpressionTooLongMsg = "formula is too long to check (limit is 4096 characters)"

// NewEnv builds the Expression drop's CEL environment: the wired value as
// `input` (dynamic — it can be anything) and the current time as `now`, with
// cross-type numeric comparisons so `input > 100` works whether input arrives
// as an int or a double. Both the drop and the linter call this.
func NewEnv() (*cel.Env, error) {
	return cel.NewEnv(
		cel.Variable("input", cel.DynType),
		cel.Variable("now", cel.TimestampType),
		cel.CrossTypeNumericComparisons(true),
		// Same string helpers as the row formulas (see rowcel.Env), so one
		// grammar is learned once and works in both places.
		ext.Strings(),
	)
}

// Issue is a single compile/type-check problem, positioned so the editor can
// point at it. Line and Column are 1-based; both are 0 when cel-go couldn't
// attribute a location.
type Issue struct {
	Message string `json:"message"`
	Line    int    `json:"line"`
	Column  int    `json:"column"`
}

// Validate compiles expr against the Expression env and returns the first
// problem, or nil when the formula is valid. Compile is a full parse +
// type-check, so it catches syntax errors, unknown functions, arity mistakes,
// and type errors — everything short of runtime data issues. An empty
// expression is "not valid yet" but not an error to surface, so it returns nil
// (the drop's own required-param check handles emptiness at run time).
func Validate(expr string) (*Issue, error) {
	if strings.TrimSpace(expr) == "" {
		return nil, nil
	}
	// Length gate BEFORE Compile — the point is to not hand an unbounded
	// expression to the parser. Reported as an Issue (not a Go error) so the
	// caller renders it inline like any other problem with the formula.
	if len(expr) > MaxExpressionLen {
		return &Issue{Message: errExpressionTooLongMsg}, nil
	}
	env, err := NewEnv()
	if err != nil {
		return nil, err
	}
	_, issues := env.Compile(expr)
	if issues == nil || issues.Err() == nil {
		return nil, nil
	}
	errs := issues.Errors()
	if len(errs) == 0 {
		// No structured errors but Err() is non-nil — surface it whole.
		return &Issue{Message: issues.Err().Error()}, nil
	}
	e := errs[0]
	return &Issue{
		Message: e.Message,
		Line:    e.Location.Line(),
		Column:  e.Location.Column() + 1, // cel columns are 0-based
	}, nil
}
