// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
	"git.sr.ht/~klahr/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "compare",
			Version:     "2.0",
			Label:       "Compare",
			Icon:        "equal",
			Category:    "flow_control",
			Provider:    "internal",
			Tags:        []string{"condition", "predicate", "boolean", "test", "compare"},
			Description: "Compare two values, A and B, and emit true or false on the Yes/No output. Pick the test from a plain-language list — equals, is greater than, contains, is one of, is within range, and more. Wire A and B from upstream steps, or type a literal default right on the step. Pair the Yes/No output with a Branch step (connect Yes/No into Branch's condition input) to route.",
			Summary:     "Compare A against B with a chosen operator and emit true or false on the Yes/No output.",
			Examples: []core.ParamsExample{
				{
					Title:  "Is the value over a threshold?",
					Params: json.RawMessage(`{"op":"greater_than","B":1000}`),
					Notes:  "Wire the number into A; B is the literal 1000. It's true when A > 1000.",
				},
				{
					Title:  "Status is one of an accepted set",
					Params: json.RawMessage(`{"op":"one_of","B":[200,201,204]}`),
					Notes:  "For one_of, B is a list — it's true when A equals any element.",
				},
				{
					Title:  "Was it a 2xx success? (range)",
					Params: json.RawMessage(`{"op":"in_range","B":[200,299]}`),
					Notes:  "in_range matches a contiguous numeric range; both ends inclusive by default. Wire the status into A.",
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			// A binary comparator: A and B are the two operands. Either can be
			// wired from upstream or typed as a literal default on the node
			// (the matching A/B params back the unconnected-pin editor).
			Inputs: []core.Port{
				{Port: "A", Label: "A"},
				{Port: "B", Label: "B"},
			},
			Outputs: []core.Port{{
				Port:  "result",
				Label: "Yes/No",
				MIME:  []string{core.MIMEBool},
			}},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"op":{
						"type":"string",
						"default":"equals",
						"title":"Test",
						"description":"How to compare A against B.",
						"enum":["equals","not_equals","greater_than","greater_or_equal","less_than","less_or_equal","contains","not_contains","one_of","not_one_of","in_range","not_in_range","exists","not_exists"],
						"enumNames":["A equals B","A does not equal B","A is greater than B","A is greater than or equal to B","A is less than B","A is less than or equal to B","A contains B","A does not contain B","A is one of B","A is not one of B","A is within range B","A is outside range B","A has a value","A is empty"]
					},
					"A":{"type":"string","title":"A","description":"Literal value for A when the A input isn't wired. Parsed as JSON when possible (e.g. 200, true), otherwise treated as text."},
					"B":{"type":"string","title":"B","description":"Literal value for B when the B input isn't wired. Parsed as JSON when possible — a number, or a list like [200,201,204] for one_of, or [min,max] for in_range."},
					"field":{"type":"string","title":"Field in A","description":"Optional dot-path into A when A is a JSON object (e.g. priority). Empty compares the whole value.","x_advanced":true},
					"inclusive_min":{"type":"boolean","default":true,"description":"For in_range: include the lower bound. Defaults to true (like Unreal's InRange).","x_advanced":true},
					"inclusive_max":{"type":"boolean","default":true,"description":"For in_range: include the upper bound. Defaults to true (like Unreal's InRange).","x_advanced":true}
				},
				"required":["op"]
			}`),
			Idempotent: true,
			// Pure predicate: emits a boolean verdict, not a payload to thread.
			NoPassthrough: true,
		},
		Execute: executeCompare,
	})
}

// executeCompare evaluates "A <op> B" and emits true or false on the
// result port. It's the "check" half of the Unreal-Blueprint split: Compare
// decides, Branch routes. Each operand comes from its input port when wired,
// or from the matching literal param (typed on the node) otherwise.
func executeCompare(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	// op defaults to "equals" (the schema default) so a freshly-dropped
	// Compare is immediately valid — no required-param friction.
	op, _ := job.Params["op"].(string)
	if op == "" {
		op = "equals"
	}
	return compareWith(job, op)
}

// compareWith runs the comparison for an explicit operator. It's the shared
// evaluator behind both the multi-op Compare node (which reads op from a
// dropdown param) and the primitive operator drops in operators.go (>, <, ==,
// …, which bake a fixed op). Keeping it in one place means a primitive can
// never drift from Compare's semantics — they ARE Compare, minus the
// dropdown.
func compareWith(job core.Job, op string) (core.Result, error) {
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

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"result": {MIME: core.MIMEBool, Inline: matched},
		},
	}, nil
}

// operand resolves an operand: the input port's inline value when wired,
// otherwise the matching literal param (coerced from its typed-in string).
func operand(job core.Job, port string) any {
	if ref, ok := job.Input[port]; ok {
		return ref.Inline
	}
	return coerceLiteral(job.Params[port])
}

// coerceLiteral turns a typed-in param into a real value. A param is a string
// from the node's text field; we JSON-parse it so "299" becomes a number,
// "[200,299]" becomes a list, and "true" becomes a bool — falling back to the
// raw string when it isn't valid JSON. Empty means "no value" (nil).
func coerceLiteral(v any) any {
	s, ok := v.(string)
	if !ok {
		return v // already a real value (wired, or a non-string param)
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var parsed any
	if err := json.Unmarshal([]byte(s), &parsed); err == nil {
		return parsed
	}
	return s
}

// extractPath navigates a dotted path inside a value. An empty field returns
// the value unchanged. A JSON string is decoded first so a field can be read
// out of a structured payload that arrived as text.
func extractPath(root any, field string) (any, error) {
	if field == "" {
		return root, nil
	}
	if root == nil {
		return nil, fmt.Errorf("A has no value to extract field %q from", field)
	}
	if s, ok := root.(string); ok {
		var v any
		if err := json.Unmarshal([]byte(s), &v); err == nil {
			root = v
		} else {
			return nil, fmt.Errorf("A is a string but not JSON; cannot extract field %q", field)
		}
	}
	current := root
	for _, part := range strings.Split(field, ".") {
		m, ok := current.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("field %q: navigated to non-object at %q", field, part)
		}
		v, exists := m[part]
		if !exists {
			return nil, nil // missing field — let exists/not_exists handle it
		}
		current = v
	}
	return current, nil
}

func evaluate(a, b any, op string, incMin, incMax bool) (bool, error) {
	switch op {
	case "exists":
		return a != nil, nil
	case "not_exists":
		return a == nil, nil
	case "equals":
		return looseEqual(a, b), nil
	case "not_equals":
		return !looseEqual(a, b), nil
	case "contains":
		sa, sb, err := stringPair(a, b, "contains")
		if err != nil {
			return false, err
		}
		return strings.Contains(sa, sb), nil
	case "not_contains":
		sa, sb, err := stringPair(a, b, "not_contains")
		if err != nil {
			return false, err
		}
		return !strings.Contains(sa, sb), nil
	case "less_than":
		return numericCompare(a, b, -1)
	case "greater_than":
		return numericCompare(a, b, 1)
	case "less_or_equal":
		return numericCompareLE(a, b)
	case "greater_or_equal":
		return numericCompareGE(a, b)
	case "one_of":
		return inSet(a, b)
	case "not_one_of":
		in, err := inSet(a, b)
		return !in, err
	case "in_range":
		return inRange(a, b, incMin, incMax)
	case "not_in_range":
		in, err := inRange(a, b, incMin, incMax)
		return !in, err
	default:
		return false, fmt.Errorf("unknown op %q", op)
	}
}

// inSet reports whether a loosely equals any element of b, which must be an
// array. The set-membership op that lets one Compare match against an
// unbounded list of accepted values (e.g. status one_of 200/201/204).
func inSet(a, b any) (bool, error) {
	arr, ok := b.([]any)
	if !ok {
		return false, fmt.Errorf("one_of requires B to be a list, got %T", b)
	}
	for _, e := range arr {
		if looseEqual(a, e) {
			return true, nil
		}
	}
	return false, nil
}

// inRange reports whether a falls between the two bounds in the [min, max]
// array B. Modelled on Unreal's InRange node: by default both ends are
// inclusive (so [200, 299] matches every 2xx HTTP status), and each end can be
// made exclusive independently via inclusive_min / inclusive_max.
func inRange(a, b any, incMin, incMax bool) (bool, error) {
	arr, ok := b.([]any)
	if !ok || len(arr) != 2 {
		return false, fmt.Errorf("in_range requires B to be a [min, max] list, got %T", b)
	}
	v, ok := toFloat(a)
	if !ok {
		return false, fmt.Errorf("in_range needs a numeric A, got %T", a)
	}
	lo, ok1 := toFloat(arr[0])
	hi, ok2 := toFloat(arr[1])
	if !ok1 || !ok2 {
		return false, fmt.Errorf("in_range bounds must both be numbers, got [%T, %T]", arr[0], arr[1])
	}
	if lo > hi {
		return false, fmt.Errorf("in_range min (%v) is greater than max (%v)", lo, hi)
	}
	lowOK := v > lo || (incMin && v == lo)
	highOK := v < hi || (incMax && v == hi)
	return lowOK && highOK, nil
}

// inclusiveFlag reads an inclusive_min/inclusive_max toggle, defaulting to
// true to match Unreal's InRange (both ends inclusive unless told otherwise).
func inclusiveFlag(p map[string]any, key string) bool {
	if b, ok := p[key].(bool); ok {
		return b
	}
	return true
}

// stringPair coerces both operands to text for the contains operators.
// Scalars (text, numbers, booleans) stringify; nil and containers (lists,
// objects) are an explicit error rather than silently becoming "" — which
// would make `contains` evaluate strings.Contains("", "") == true and
// silently misroute a Branch wired to the Yes/No output.
func stringPair(a, b any, op string) (string, string, error) {
	sa, oka := toStr(a)
	sb, okb := toStr(b)
	if !oka || !okb {
		return "", "", fmt.Errorf("%s needs text values, got %T and %T", op, a, b)
	}
	return sa, sb, nil
}

func toStr(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case bool:
		return fmt.Sprintf("%t", x), true
	case json.Number:
		return x.String(), true
	case int, int32, int64, float32, float64:
		return fmt.Sprintf("%v", x), true
	}
	return "", false
}

func looseEqual(a, b any) bool {
	// Handle the common JSON-decoded number cases. JSON only has
	// "number" and Go decodes everything to float64; user-typed JSON
	// like 200 arrives as float64 too. Compare numerically when both
	// sides convert.
	an, aok := toFloat(a)
	bn, bok := toFloat(b)
	if aok && bok {
		return an == bn
	}
	// reflect.DeepEqual rather than `==`: the operands are arbitrary
	// JSON-decoded values, and `==` panics when both sides are the same
	// uncomparable type (a slice or map — e.g. two wired-in rows arrays).
	// DeepEqual gives sensible structural equality and never panics.
	return reflect.DeepEqual(a, b)
}

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case string:
		// Plenty of steps report a number as text — a count pin, a status
		// code, a spreadsheet cell. Comparing one of those against a number
		// is the obvious thing to do, and failing it with "string vs
		// float64" is an error the person who wired it can do nothing with.
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f, err == nil
	case []byte:
		f, err := strconv.ParseFloat(strings.TrimSpace(string(x)), 64)
		return f, err == nil
	case int:
		return float64(x), true
	case int32:
		return float64(x), true
	case int64:
		return float64(x), true
	case float32:
		return float64(x), true
	case float64:
		return x, true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	}
	return 0, false
}

// numericCompare returns true when sign(a - b) matches `want` (-1 for <, +1
// for >).
func numericCompare(a, b any, want int) (bool, error) {
	af, ok1 := toFloat(a)
	bf, ok2 := toFloat(b)
	if !ok1 || !ok2 {
		return false, fmt.Errorf("non-numeric operand in <,> comparison: %T vs %T", a, b)
	}
	switch {
	case af < bf:
		return want == -1, nil
	case af > bf:
		return want == 1, nil
	default:
		return false, nil
	}
}

func numericCompareLE(a, b any) (bool, error) {
	af, ok1 := toFloat(a)
	bf, ok2 := toFloat(b)
	if !ok1 || !ok2 {
		return false, fmt.Errorf("non-numeric operand in <= comparison: %T vs %T", a, b)
	}
	return af <= bf, nil
}

func numericCompareGE(a, b any) (bool, error) {
	af, ok1 := toFloat(a)
	bf, ok2 := toFloat(b)
	if !ok1 || !ok2 {
		return false, fmt.Errorf("non-numeric operand in >= comparison: %T vs %T", a, b)
	}
	return af >= bf, nil
}
