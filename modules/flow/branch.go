package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/engine"
)

func init() {
	engine.Register(engine.NativeNode{
		Manifest: core.Manifest{
			ID:             "branch",
			Version:        "1.0",
			Label:          "Branch",
			Color:          "#5a9bd4",
			Icon:           "git-branch",
			Category:       "flow_control",
			Provider:       "internal",
			Tags:           []string{"conditional", "routing", "if-else"},
			Description:    "Route input to either the then or else output port based on a structured condition (equals/greater_than/contains/exists/...). Field paths into JSON inputs are supported.",
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs:         []core.Port{{Port: "in", Required: true, Label: "Value to test"}},
			Outputs: []core.Port{
				{Port: "then", Label: "Taken when the condition is true"},
				{Port: "else", Label: "Taken when the condition is false"},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"condition":{
						"type":"object",
						"description":"Test applied to the upstream input. The matching record routes to the 'then' port; the failing one (or any other) routes to 'else'.",
						"properties":{
							"field":{"type":"string","description":"Dot-path into the input value. Empty matches against the whole input."},
							"op":{"type":"string","enum":["equals","not_equals","less_than","greater_than","less_or_equal","greater_or_equal","contains","not_contains","exists","not_exists"],"description":"Comparison operator."},
							"value":{
								"description":"Comparison target. Type must match the field being compared.",
								"oneOf":[
									{"type":"string","title":"String"},
									{"type":"number","title":"Number"},
									{"type":"boolean","title":"Boolean"}
								]
							}
						},
						"required":["op"]
					}
				},
				"required":["condition"]
			}`),
			Idempotent: true,
		},
		Execute: executeBranch,
	})
}

type branchCondition struct {
	Field string
	Op    string
	Value any
}

// executeBranch evaluates a single structured condition against the input
// ref and emits the input on either the "then" or "else" output port —
// never both. Downstream nodes wired to the unused port are dormant
// (the engine treats missing-port-output as a skip-blocking edge).
//
// Why not a full expression language (CEL, JQ, JS)? It's the right
// upgrade path but every choice has tradeoffs and trapdoor edges.
// Structured conditions cover ~80% of practical routing cases and
// keep the graph JSON readable.
func executeBranch(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	input, ok := job.Input["in"]
	if !ok {
		return errResult(job, "missing_input", "input port 'in' is required"), nil
	}
	condRaw, ok := job.Params["condition"]
	if !ok {
		return errResult(job, "bad_param", "param 'condition' is required"), nil
	}
	cond, err := parseCondition(condRaw)
	if err != nil {
		return errResult(job, "bad_param", err.Error()), nil
	}

	value, err := extractField(input, cond.Field)
	if err != nil {
		return errResult(job, "bad_input", err.Error()), nil
	}

	matched, err := evaluate(value, cond.Op, cond.Value)
	if err != nil {
		return errResult(job, "bad_param", err.Error()), nil
	}

	output := map[string]core.Ref{}
	if matched {
		output["then"] = input
	} else {
		output["else"] = input
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: output,
	}, nil
}

func parseCondition(raw any) (branchCondition, error) {
	m, ok := raw.(map[string]any)
	if !ok {
		return branchCondition{}, fmt.Errorf("condition: expected object, got %T", raw)
	}
	c := branchCondition{}
	if f, ok := m["field"].(string); ok {
		c.Field = f
	}
	op, ok := m["op"].(string)
	if !ok {
		return branchCondition{}, fmt.Errorf("condition.op: required string")
	}
	c.Op = op
	c.Value = m["value"]
	return c, nil
}

// extractField navigates a dotted path inside the input. The input is
// interpreted as JSON or a pre-decoded map. An empty field means "test
// the whole input value".
func extractField(input core.Ref, field string) (any, error) {
	if field == "" {
		return inlineValue(input), nil
	}
	root := inlineValue(input)
	if root == nil {
		return nil, fmt.Errorf("input has no inline value to extract field %q from", field)
	}
	// If root is a string that looks like JSON, decode it.
	if s, ok := root.(string); ok {
		var v any
		if err := json.Unmarshal([]byte(s), &v); err == nil {
			root = v
		} else {
			return nil, fmt.Errorf("input is a string but not JSON; cannot extract field %q", field)
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
			return nil, nil // missing field — let exists/not_exists ops handle it
		}
		current = v
	}
	return current, nil
}

func inlineValue(ref core.Ref) any {
	if ref.Inline != nil {
		return ref.Inline
	}
	return nil
}

func evaluate(value any, op string, expected any) (bool, error) {
	switch op {
	case "exists":
		return value != nil, nil
	case "not_exists":
		return value == nil, nil
	case "equals":
		return looseEqual(value, expected), nil
	case "not_equals":
		return !looseEqual(value, expected), nil
	case "contains":
		s, _ := value.(string)
		e, _ := expected.(string)
		return strings.Contains(s, e), nil
	case "not_contains":
		s, _ := value.(string)
		e, _ := expected.(string)
		return !strings.Contains(s, e), nil
	case "less_than":
		return numericCompare(value, expected, -1)
	case "greater_than":
		return numericCompare(value, expected, 1)
	case "less_or_equal":
		return numericCompareLE(value, expected)
	case "greater_or_equal":
		return numericCompareGE(value, expected)
	default:
		return false, fmt.Errorf("unknown op %q", op)
	}
}

func looseEqual(a, b any) bool {
	// Handle the common JSON-decoded number cases. JSON only has
	// "number" and Go decodes everything to float64; user-typed JSON
	// like {"value": 200} arrives as float64 too. Compare numerically
	// when both sides convert.
	an, aok := toFloat(a)
	bn, bok := toFloat(b)
	if aok && bok {
		return an == bn
	}
	return a == b
}

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
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

// numericCompare returns true when sign(value - expected) matches `want`
// (-1 for <, +1 for >).
func numericCompare(value, expected any, want int) (bool, error) {
	vf, ok1 := toFloat(value)
	ef, ok2 := toFloat(expected)
	if !ok1 || !ok2 {
		return false, fmt.Errorf("non-numeric operand in <,> comparison: %T vs %T", value, expected)
	}
	switch {
	case vf < ef:
		return want == -1, nil
	case vf > ef:
		return want == 1, nil
	default:
		return false, nil
	}
}

func numericCompareLE(value, expected any) (bool, error) {
	vf, ok1 := toFloat(value)
	ef, ok2 := toFloat(expected)
	if !ok1 || !ok2 {
		return false, fmt.Errorf("non-numeric operand in <= comparison: %T vs %T", value, expected)
	}
	return vf <= ef, nil
}

func numericCompareGE(value, expected any) (bool, error) {
	vf, ok1 := toFloat(value)
	ef, ok2 := toFloat(expected)
	if !ok1 || !ok2 {
		return false, fmt.Errorf("non-numeric operand in >= comparison: %T vs %T", value, expected)
	}
	return vf >= ef, nil
}
