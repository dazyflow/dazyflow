// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package rowcel

import (
	"strings"
	"testing"

	"github.com/google/cel-go/cel"
)

func mustEnv(t *testing.T, extra ...cel.EnvOption) *cel.Env {
	t.Helper()
	env, err := Env(extra...)
	if err != nil {
		t.Fatalf("Env: %v", err)
	}
	return env
}

func TestEnv_WithExtraVariable(t *testing.T) {
	// The extra option must be honored: a computed variable compiles.
	env := mustEnv(t, cel.Variable("score", cel.IntType))
	if _, err := Compile(env, "score > 0", "filter"); err != nil {
		t.Errorf("compile with extra var: %v", err)
	}
}

func TestVars(t *testing.T) {
	row := map[string]any{"a": 1}
	v := Vars(row)
	if _, ok := v["row"]; !ok {
		t.Error("Vars missing 'row'")
	}
	if _, ok := v["now"]; !ok {
		t.Error("Vars missing 'now'")
	}
}

func TestCompile_Error(t *testing.T) {
	env := mustEnv(t)
	_, err := Compile(env, "row.", "myfield")
	if err == nil || !strings.Contains(err.Error(), "myfield") {
		t.Errorf("compile error = %v, want labelled error", err)
	}
}

func TestEvalBool(t *testing.T) {
	env := mustEnv(t)

	// True branch.
	prog, err := Compile(env, `row.amount > 100`, "filter")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	ok, err := EvalBool(prog, map[string]any{"amount": 150})
	if err != nil || !ok {
		t.Errorf("EvalBool true = %v, %v", ok, err)
	}
	// False branch.
	ok, err = EvalBool(prog, map[string]any{"amount": 10})
	if err != nil || ok {
		t.Errorf("EvalBool false = %v, %v", ok, err)
	}
}

func TestEvalBool_NonBoolResult(t *testing.T) {
	env := mustEnv(t)
	prog, err := Compile(env, `row.name`, "filter")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	_, err = EvalBool(prog, map[string]any{"name": "ada"})
	if err == nil || !strings.Contains(err.Error(), "must return bool") {
		t.Errorf("non-bool result = %v, want bool-contract error", err)
	}
}

func TestEvalBool_EvalError(t *testing.T) {
	env := mustEnv(t)
	// Accessing a key absent from the row is a runtime no-such-key error.
	prog, err := Compile(env, `row.missing > 1`, "filter")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if _, err := EvalBool(prog, map[string]any{"present": 1}); err == nil {
		t.Error("expected eval error for missing key")
	}
}

func TestCostLimit_Constant(t *testing.T) {
	if CostLimit == 0 {
		t.Error("CostLimit must be non-zero")
	}
}

// The string helpers are what a person reaches for the first time they write
// a formula by hand — without them, "the first ten characters" or "upper-case
// it" needs a second step, or can't be said at all.
func TestEnv_StringHelpers(t *testing.T) {
	env, err := Env()
	if err != nil {
		t.Fatalf("env: %v", err)
	}
	cases := map[string]any{
		`row.text.substring(0, 7)`:                               "printer",
		`row.name.upperAscii()`:                                  "IDA",
		`row.messy.trim()`:                                       "spaced",
		`row.text.split(" ")[0]`:                                 "printer",
		`row.text.replace("jamed", "jammed").contains("jammed")`: true,
		`row.text.indexOf("floor")`:                              int64(11),
	}
	row := map[string]any{"text": "printer on floor 2 is jamed", "name": "ida", "messy": "  spaced  "}
	for expr, want := range cases {
		prog, cerr := Compile(env, expr, "test")
		if cerr != nil {
			t.Errorf("%s: %v", expr, cerr)
			continue
		}
		out, _, eerr := prog.Eval(Vars(row))
		if eerr != nil {
			t.Errorf("%s: %v", expr, eerr)
			continue
		}
		if out.Value() != want {
			t.Errorf("%s = %v (%T), want %v", expr, out.Value(), out.Value(), want)
		}
	}
}
