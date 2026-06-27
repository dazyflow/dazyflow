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
