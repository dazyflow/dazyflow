// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package flow

import (
	"context"
	"encoding/json"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
	"git.sr.ht/~klahr/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "if",
			Version:     "1.0",
			Label:       "If",
			Icon:        "git-branch",
			Category:    "flow_control",
			Provider:    "internal",
			Tags:        []string{"conditional", "filter", "if-else", "routing", "predicate"},
			Description: "Test the value on A and send it down Yes or No in one step. Pick the test from a plain-language list — equals, contains, is greater than, is one of, is within range, and more. A is both the value tested and the payload that flows on: it leaves via Yes when the test passes, via No when it fails. Connect B from an earlier step or type a literal default. This is Compare + Branch fused for the everyday single-condition case; reach for Compare → Branch when you need to route a different payload than the one you test, or to combine conditions with And/Or/Not.",
			Summary:     "Test A with a chosen operator and forward it down the Yes or No port.",
			Examples: []core.ParamsExample{
				{
					Title:  "Pass through only if the text contains a word",
					Params: json.RawMessage(`{"op":"contains","B":"urgent"}`),
					Notes:  "Connect the text into A; B is the literal \"urgent\". A leaves via Yes when it contains \"urgent\", otherwise via No.",
				},
				{
					Title:  "Route on a field of a JSON payload",
					Params: json.RawMessage(`{"op":"equals","field":"status","B":"active"}`),
					Notes:  "Tests A.status == \"active\" but routes the whole A payload down Yes/No.",
				},
				{
					Title:  "Keep only 2xx responses",
					Params: json.RawMessage(`{"op":"in_range","field":"code","B":[200,299]}`),
					Notes:  "in_range is inclusive on both ends by default.",
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			// A is both the value under test and the payload that routes on. B is
			// the (optional) comparison operand — wired or typed as a literal.
			Inputs: []core.Port{
				{Port: "A", Required: true, Label: "Value"},
				{Port: "B", Label: "Compare to"},
			},
			Outputs: []core.Port{
				{Port: "then", Label: "Yes"},
				{Port: "else", Label: "No"},
			},
			// Same test config as Compare, minus the result port: only the output
			// shape differs (route the payload vs. emit a boolean value).
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"op":{
						"type":"string",
						"default":"equals",
						"title":"Test",
						"description":"How to test A (against B).",
						"enum":["equals","not_equals","greater_than","greater_or_equal","less_than","less_or_equal","contains","not_contains","one_of","not_one_of","in_range","not_in_range","exists","not_exists"],
						"enumNames":["A equals B","A does not equal B","A is greater than B","A is greater than or equal to B","A is less than B","A is less than or equal to B","A contains B","A does not contain B","A is one of B","A is not one of B","A is within range B","A is outside range B","A has a value","A is empty"]
					},
					"B":{"type":"string","title":"B","description":"Literal value for B when the B input isn't connected. Parsed as JSON when possible — a number, or a list like [200,201,204] for one_of, or [min,max] for in_range."},
					"field":{"type":"string","title":"Field in A","description":"Optional dot-path into A when A is a JSON object (e.g. status). Empty tests the whole value. The full A payload still routes — only the test reads the field.","x_advanced":true},
					"inclusive_min":{"type":"boolean","default":true,"description":"For in_range: include the lower bound.","x_advanced":true},
					"inclusive_max":{"type":"boolean","default":true,"description":"For in_range: include the upper bound.","x_advanced":true}
				},
				"required":["op"]
			}`),
			Idempotent: true,
			// Pure router (like Branch): a passthrough would emit on every success
			// and so fire BOTH ports. Route only via A → then/else.
			NoPassthrough: true,
		},
		Execute: executeIf,
	})

	// Contains — a fixed-operator preset of If, the way operators.go bakes a
	// single op onto Compare. The op is the node's identity (not a dropdown),
	// the pins read as Text/Substring, and the outputs say what they mean. It's
	// the same router underneath: routeByCompare(job, "contains").
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "contains",
			Version:     "1.0",
			Label:       "Contains",
			Icon:        "search",
			Category:    "flow_control",
			Provider:    "internal",
			Tags:        []string{"conditional", "filter", "text", "string", "substring", "search", "routing"},
			Description: "Check whether the Text on A contains the Substring on B, and send the text down Yes or No accordingly. A fixed-test preset of the If step — the test is always 'contains', so there's nothing to configure but the substring. Connect the text into A and type (or connect) the substring into B. For other tests (equals, ranges, one of), use If or Compare.",
			Summary:     "Forward the Text down Yes or No based on whether it contains the Substring.",
			Examples: []core.ParamsExample{
				{
					Title:  "Keep only messages mentioning a word",
					Params: json.RawMessage(`{"B":"urgent"}`),
					Notes:  `Wire the message text into A; B is the literal "urgent". The text leaves via Yes when it includes "urgent", otherwise via No.`,
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "A", Required: true, Label: "Text", MIME: []string{"text/plain"}},
				{Port: "B", Label: "Substring", MIME: []string{"text/plain"}},
			},
			Outputs: []core.Port{
				{Port: "then", Label: "Yes"},
				{Port: "else", Label: "No"},
			},
			// Just the substring to look for — no op enum (the node IS contains),
			// no field/range knobs. Keeping the preset trivial is the point.
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"B":{"type":"string","title":"Substring","description":"The text to look for inside A, when the B input isn't connected."}
				}
			}`),
			Idempotent:    true,
			NoPassthrough: true,
		},
		Execute: executeContains,
	})
}

// executeContains is the Contains preset's executor: If's router with the
// operator locked to "contains".
func executeContains(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	return routeByCompare(job, "contains")
}

// executeIf is Compare + Branch fused: it evaluates "A <op> B" using Compare's
// exact evaluator (operand/extractPath/evaluate), then forwards the A payload
// down "then" (Yes) or "else" (No) — never both. Reusing compare.go's evaluator
// means If can never drift from Compare's semantics; it only swaps the boolean
// result for a routed payload. Downstream nodes on the unused port stay dormant.
func executeIf(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	// op defaults to "equals" (the schema default) so a freshly-dropped If is
	// immediately valid — no required-param friction.
	op, _ := job.Params["op"].(string)
	if op == "" {
		op = "equals"
	}
	return routeByCompare(job, op)
}

// routeByCompare is the shared core behind If (op from a dropdown) and the
// fixed-operator presets like Contains (op baked into the node's identity).
// Keeping it in one place means a preset can never drift from If/Compare.
func routeByCompare(job core.Job, op string) (core.Result, error) {
	field, _ := job.Params["field"].(string)
	a, err := extractPath(operand(job, "A"), field)
	if err != nil {
		return params.Err(job, "bad_input", err.Error()), nil
	}
	b := operand(job, "B")

	matched, err := evaluate(a, b, op,
		inclusiveFlag(job.Params, "inclusive_min"),
		inclusiveFlag(job.Params, "inclusive_max"))
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}

	// Route the original A ref so MIME/blob pointers survive; when A is a
	// literal-only (unwired) operand there's no ref to forward, so synthesize
	// an inline one from the resolved value.
	payload, ok := job.Input["A"]
	if !ok {
		payload = core.Ref{Inline: a}
	}

	out := map[string]core.Ref{}
	if matched {
		out["then"] = payload
	} else {
		out["else"] = payload
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: out,
	}, nil
}
