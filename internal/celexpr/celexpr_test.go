// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package celexpr

import "testing"

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
