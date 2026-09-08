// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

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

const errExpressionTooLongMsg = "formula is too long to check (limit is 4096 characters)"

func NewEnv() (*cel.Env, error) {
	return cel.NewEnv(
		cel.Variable("input", cel.DynType),
		cel.Variable("now", cel.TimestampType),
		cel.CrossTypeNumericComparisons(true),
		ext.Strings(),
	)
}

type Issue struct {
	Message string `json:"message"`
	Line    int    `json:"line"`
	Column  int    `json:"column"`
}

func Validate(expr string) (*Issue, error) {
	if strings.TrimSpace(expr) == "" {
		return nil, nil
	}
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
		return &Issue{Message: issues.Err().Error()}, nil
	}
	e := errs[0]
	return &Issue{
		Message: e.Message,
		Line:    e.Location.Line(),
		Column:  e.Location.Column() + 1, // cel columns are 0-based
	}, nil
}
