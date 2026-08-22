// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package transform

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/engine"
	"git.sr.ht/~klahr/dazyflow/internal/celexpr"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "expression",
			Version:     "1.0",
			Label:       "Expression",
			Subtitle:    "Compute a value with a formula",
			Icon:        "function-square",
			Category:    "transformation",
			Provider:    "internal",
			Tags:        []string{"expression", "formula", "cel", "code", "compute", "logic", "transform"},
			Description: "Compute a single value from a formula — the value-level companion to the calculated-column step. Write one CEL expression (the same formula language the row tools use) with the connected 'in' value available as `input` and the current time as `now`. Use it to reshape a value mid-flow: pull a field (`input.user.email`), do arithmetic (`input * 1.25`), build a string (`\"Hi \" + input.name`), test a condition (`input.status == \"paid\"`), or transform a list (`input.map(x, x.id)`). The result is emitted on 'out', typed by what the formula returns (text, boolean, or JSON). For running real OS commands or scripts, use the Shell step instead — this is a safe, sandboxed expression evaluator, not a general code runtime.",
			Summary:     "Evaluate one CEL formula over the input value and emit the result.",
			Examples: []core.ParamsExample{
				{
					Title:  "Multiply a number",
					Params: json.RawMessage(`{"expr":"input * 1.25"}`),
					Notes:  "Connect a number into 'in'; this adds 25%.",
				},
				{
					Title:  "Pull a nested field out of an object",
					Params: json.RawMessage(`{"expr":"input.user.email"}`),
				},
				{
					Title:  "Build a yes/no condition",
					Params: json.RawMessage(`{"expr":"input.status == 'paid' && input.total > 100"}`),
					Notes:  "Returns a boolean on 'out' — connect it into a Branch.",
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "in", Label: "Input"},
			},
			Outputs: []core.Port{
				{Port: "out", Label: "Result"},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"expr":{"type":"string","format":"multiline","x_cel":true,"title":"Formula (CEL)","description":"A CEL expression. The variable 'input' is the connected value and 'now' is the current timestamp; the expression's value is emitted on 'out'."}
				},
				"required":["expr"]
			}`),
			Idempotent: true,
		},
		Execute: executeExpression,
	})
}

// executeExpression compiles and evaluates the `expr` CEL formula with the
// wired 'in' value bound to `input` (and `now` to the current time), then
// emits the result — typed text/plain for a string, the bool MIME for a
// boolean, application/json otherwise. It shares the row tools' compile path
// (compileRowExpr → the rowcel cost ceiling) and result unwrapper (unwrapCEL),
// so the value-level formula and the row-level formula behave identically.
func executeExpression(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	exprStr, ok := job.Params["expr"].(string)
	if !ok || strings.TrimSpace(exprStr) == "" {
		return errResult(job, "bad_param", "param 'expr' is required (a CEL formula)"), nil
	}

	// Same length gate the linter applies, so the two stay in lockstep: a
	// formula the linter refuses to check must not compile here either.
	if len(exprStr) > celexpr.MaxExpressionLen {
		return errResult(job, "bad_param", fmt.Sprintf(
			"formula is %d characters; the limit is %d", len(exprStr), celexpr.MaxExpressionLen)), nil
	}
	// Shared with the editor's linter (POST /tools/expression/validate) via
	// internal/celexpr, so what the linter accepts is exactly what runs here.
	env, err := celexpr.NewEnv()
	if err != nil {
		return errResult(job, "internal", fmt.Sprintf("cel env: %v", err)), nil
	}
	prog, err := compileRowExpr(env, exprStr, "expression")
	if err != nil {
		return errResult(job, "bad_param", err.Error()), nil
	}

	var input any
	if ref, ok := job.Input["in"]; ok {
		input = ref.Inline
	}

	v, _, err := prog.Eval(map[string]any{"input": input, "now": time.Now().UTC()})
	if err != nil {
		return errResult(job, "eval", err.Error()), nil
	}
	result, err := unwrapCEL(v)
	if err != nil {
		return errResult(job, "eval", err.Error()), nil
	}

	mime := "application/json"
	switch result.(type) {
	case string:
		mime = "text/plain"
	case bool:
		mime = core.MIMEBool
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"out": {MIME: mime, Inline: result},
		},
	}, nil
}
