// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package celexpr

import (
	"strings"
	"testing"
)

func TestValidate(t *testing.T) {
	cases := []struct {
		name    string
		expr    string
		wantErr bool
	}{
		{"empty is not an error", "", false},
		{"blank is not an error", "   ", false},
		{"arithmetic on input", "input * 1.25", false},
		{"field access", "input.user.email", false},
		{"boolean condition", "input.status == 'paid' && input.total > 100", false},
		{"list macro", "input.map(x, x.id)", false},
		{"now is available", "string(now)", false},
		{"syntax error", "input +", true},
		{"unbalanced paren", "size(input", true},
		{"unknown variable", "nope + 1", true},
		{"unknown function", "frobnicate(input)", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			issue, err := Validate(tc.expr)
			if err != nil {
				t.Fatalf("Validate returned an env error: %v", err)
			}
			if tc.wantErr && issue == nil {
				t.Errorf("expected a lint issue for %q, got none", tc.expr)
			}
			if !tc.wantErr && issue != nil {
				t.Errorf("expected %q to be valid, got issue %+v", tc.expr, issue)
			}
		})
	}
}

// TestValidate_MatchesDropEnv guards the drift the shared env exists to
// prevent: `now` and cross-type numeric comparison must both be part of the
// env the linter uses, exactly as the drop relies on them.
func TestValidate_MatchesDropEnv(t *testing.T) {
	// cross-type numeric comparison: int input vs double literal.
	if issue, _ := Validate("input > 1.5"); issue != nil {
		t.Errorf("cross-type numeric comparison should validate, got %+v", issue)
	}
}

// The Expression drop shares the row formulas' string helpers, so one grammar
// is learned once and works in both places.
func TestNewEnv_StringHelpers(t *testing.T) {
	if issue, _ := Validate(`input.substring(0, 3).upperAscii() + input.split(" ")[1].trim()`); issue != nil {
		t.Errorf("string helpers should compile: %+v", issue)
	}
}

// TestValidate_LengthGate covers the cap that exists so an unbounded
// expression never reaches the parser at all. The gate is checked BEFORE
// Compile, so an over-long formula costs nothing to reject — and it is
// reported as an Issue rather than a Go error so the editor renders it inline
// beside any other problem with the formula.
func TestValidate_LengthGate(t *testing.T) {
	// Valid CEL, just too much of it: the gate must fire on length, not on
	// the expression being malformed.
	long := "input + " + strings.Repeat("1 + ", MaxExpressionLen) + "1"
	if len(long) <= MaxExpressionLen {
		t.Fatalf("test fixture is only %d chars, need > %d", len(long), MaxExpressionLen)
	}
	issue, err := Validate(long)
	if err != nil {
		t.Fatalf("the length gate must not surface a Go error: %v", err)
	}
	if issue == nil {
		t.Fatal("an over-long formula was accepted")
	}
	if !strings.Contains(issue.Message, "too long") {
		t.Errorf("message = %q, want it to say the formula is too long", issue.Message)
	}
	// No location: there is no position to point at when nothing was parsed.
	if issue.Line != 0 || issue.Column != 0 {
		t.Errorf("issue carries a location %d:%d, want none", issue.Line, issue.Column)
	}

	// Exactly at the limit is still checked normally, so the boundary is
	// inclusive and a formula right at the cap is not rejected out of hand.
	atLimit := "input" + strings.Repeat(" ", MaxExpressionLen-len("input"))
	if len(atLimit) != MaxExpressionLen {
		t.Fatalf("fixture is %d chars, want exactly %d", len(atLimit), MaxExpressionLen)
	}
	if issue, err := Validate(atLimit); err != nil || issue != nil {
		t.Errorf("a formula exactly at the cap was rejected: issue=%+v err=%v", issue, err)
	}
}

// TestValidate_IssueLocation pins the 1-based column translation. CEL reports
// columns 0-based; the editor's gutter is 1-based, so an off-by-one here puts
// the caret on the wrong character.
func TestValidate_IssueLocation(t *testing.T) {
	issue, err := Validate("input + ")
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if issue == nil {
		t.Fatal("expected an issue")
	}
	if issue.Line != 1 {
		t.Errorf("Line = %d, want 1", issue.Line)
	}
	if issue.Column < 1 {
		t.Errorf("Column = %d, want 1-based (never 0)", issue.Column)
	}
	if issue.Message == "" {
		t.Error("issue has no message")
	}
}
