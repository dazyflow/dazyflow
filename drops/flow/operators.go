package flow

import (
	"context"
	"encoding/json"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/drops/internal/params"
	"git.sr.ht/~klahr/hazyflow/engine"
)

// Primitive comparison drops — the "super basic" split of the Compare node,
// one node per operator, modelled on Unreal Blueprint's individual
// comparison nodes (==, !=, >, >=, <, <=). Each bakes a fixed operator and
// delegates to compareWith (compare.go), so there is exactly one evaluator:
// a primitive can never drift from Compare's semantics — it IS Compare, with
// the operator as the node's identity instead of a dropdown.
//
// They share Compare's shape — two operand pins A and B (wire them, or type a
// literal default), and a Result port emitting a boolean to feed Branch. The win is
// reading speed: an "A > B" node says what it does at a glance, no need to
// open it and read the dropdown. The full Compare stays the power node for
// the long tail (contains, one_of, exists).
//
// In Range is the one ternary member, modelled on Unreal's InRange node:
// three value pins (Value, Min, Max) plus advanced InclusiveMin/InclusiveMax
// toggles. It can't ride registerOperator's binary mold, so it has its own
// schema and a thin executor that packs [Min, Max] into the list B that
// evaluate's in_range op expects — still one evaluator, no drift.
//
// Category "logic" (distinct from flow_control) buckets these pure
// predicates; the UI tints the whole category one color, Blueprint-style.

// operandSchema is the trimmed params schema: just the A/B literal defaults
// for unwired pins. No op enum (the node IS the op) and none of Compare's
// advanced field/range knobs — keeping these "super basic" is the point.
var operandSchema = json.RawMessage(`{
	"type":"object",
	"properties":{
		"A":{"type":"string","title":"A","description":"Literal value for A when the A input isn't wired. Parsed as JSON when possible (e.g. 200, true), otherwise treated as text."},
		"B":{"type":"string","title":"B","description":"Literal value for B when the B input isn't wired. Parsed as JSON when possible (e.g. 1000)."}
	}
}`)

// operatorSpec is the per-drop variation; everything else is identical and
// supplied by registerOperator.
type operatorSpec struct {
	id      string
	label   string
	icon    string
	op      string // the Compare operator this node bakes in
	numeric bool   // true for ops whose operands must be numbers (>, >=, <, <=)
	summary string
	desc    string
	example core.ParamsExample
}

// operandPorts builds the A/B input ports. Numeric ops type them
// application/json (the number type — blue) so they read as number pins and
// wire from Number / status; equality ops leave them untyped (any value), so
// the pin is neutral and accepts strings, numbers, or bools alike.
func operandPorts(numeric bool) []core.Port {
	var mime []string
	if numeric {
		mime = []string{"application/json"}
	}
	return []core.Port{
		{Port: "A", Label: "A", MIME: mime},
		{Port: "B", Label: "B", MIME: mime},
	}
}

func registerOperator(o operatorSpec) {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:       o.id,
			Version:  "1.0",
			Label:    o.label,
			Icon:     o.icon,
			Category: "logic",
			Provider: "internal",
			// Color is intentionally unset: the UI tints "logic" drops from
			// the category palette, the way Blueprint colors pure nodes.
			Tags:           []string{"condition", "predicate", "boolean", "compare", "logic", o.op},
			Description:    o.desc,
			Summary:        o.summary,
			Examples:       []core.ParamsExample{o.example},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs:         operandPorts(o.numeric),
			Outputs:        []core.Port{{Port: "result", Label: "Result", MIME: []string{core.MIMEBool}}},
			ParamsSchema:   operandSchema,
			Idempotent:     true,
			// Pure predicate: no payload to thread, and on the chip the pass
			// pin would steal an operand slot (it renders pins positionally).
			NoPassthrough: true,
		},
		Execute: func(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
			return compareWith(job, o.op)
		},
	})
}

func init() {
	registerOperator(operatorSpec{
		id: "eq", label: "A = B", icon: "equal", op: "equals",
		summary: "Emit true when A equals B, else false.",
		desc:    "Emit true on Result when A equals B, otherwise false. The atomic equality node — wire A and B, or type literal defaults. Pair Result with Branch to route. Reach for Compare instead when you need richer tests (contains, one_of, ranges).",
		example: core.ParamsExample{Title: "Status equals 200", Params: json.RawMessage(`{"B":200}`), Notes: "Wire the status into A; B is the literal 200. Result is true when A == 200."},
	})
	registerOperator(operatorSpec{
		id: "neq", label: "A ≠ B", icon: "equal-not", op: "not_equals",
		summary: "Emit true when A does not equal B, else false.",
		desc:    "Emit true on Result when A does not equal B, otherwise false. Pair Result with Branch to route.",
		example: core.ParamsExample{Title: "State is not idle", Params: json.RawMessage(`{"B":"idle"}`), Notes: `Result is true when A is anything other than "idle".`},
	})
	registerOperator(operatorSpec{
		id: "gt", label: "A > B", icon: "chevron-right", op: "greater_than", numeric: true,
		summary: "Emit true when A is greater than B, else false.",
		desc:    "Emit true on Result when numeric A is strictly greater than B, otherwise false. Both operands must be numbers. Pair Result with Branch to route.",
		example: core.ParamsExample{Title: "Over a threshold", Params: json.RawMessage(`{"B":1000}`), Notes: "Wire the number into A; B is the literal 1000. Result is true when A > 1000."},
	})
	registerOperator(operatorSpec{
		id: "gte", label: "A ≥ B", icon: "chevrons-right", op: "greater_or_equal", numeric: true,
		summary: "Emit true when A is greater than or equal to B, else false.",
		desc:    "Emit true on Result when numeric A is greater than or equal to B, otherwise false. Pair Result with Branch to route.",
		example: core.ParamsExample{Title: "At least N items", Params: json.RawMessage(`{"B":3}`), Notes: "Result is true when A >= 3."},
	})
	registerOperator(operatorSpec{
		id: "lt", label: "A < B", icon: "chevron-left", op: "less_than", numeric: true,
		summary: "Emit true when A is less than B, else false.",
		desc:    "Emit true on Result when numeric A is strictly less than B, otherwise false. Both operands must be numbers. Pair Result with Branch to route.",
		example: core.ParamsExample{Title: "Under a limit", Params: json.RawMessage(`{"B":100}`), Notes: "Result is true when A < 100."},
	})
	registerOperator(operatorSpec{
		id: "lte", label: "A ≤ B", icon: "chevrons-left", op: "less_or_equal", numeric: true,
		summary: "Emit true when A is less than or equal to B, else false.",
		desc:    "Emit true on Result when numeric A is less than or equal to B, otherwise false. Pair Result with Branch to route.",
		example: core.ParamsExample{Title: "No more than N", Params: json.RawMessage(`{"B":5}`), Notes: "Result is true when A <= 5."},
	})

	// In Range — the ternary primitive (see file header). Value/Min/Max pins,
	// inclusive bounds by default, delegating to the same evaluate() as Compare.
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "in_range",
			Version:     "1.0",
			Label:       "In Range",
			Icon:        "brackets",
			Category:    "logic",
			Provider:    "internal",
			Tags:        []string{"condition", "predicate", "boolean", "compare", "logic", "in_range", "range", "between"},
			Description: "Emit true on Result when numeric Value falls between Min and Max, otherwise false. Both bounds are inclusive by default (so Min=200, Max=299 matches every 2xx status); toggle InclusiveMin/InclusiveMax to make either end exclusive. Modelled on Unreal Blueprint's InRange node. Wire Value/Min/Max from upstream or type literal defaults; pair Result with Branch to route.",
			Summary:     "Emit true when Min ≤ Value ≤ Max (bounds inclusive by default), else false.",
			Examples: []core.ParamsExample{{
				Title:  "Was it a 2xx success?",
				Params: json.RawMessage(`{"min":200,"max":299}`),
				Notes:  "Wire the status into Value; Min and Max are the literals 200 and 299. Result is true for 200–299 inclusive.",
			}},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			// Numeric operands: typed application/json (the number type here —
			// the Number drop and HTTP's status code both emit it) so the pins
			// read blue and wire cleanly from those sources.
			Inputs: []core.Port{
				{Port: "value", Label: "Value", MIME: []string{"application/json"}},
				{Port: "min", Label: "Min", MIME: []string{"application/json"}},
				{Port: "max", Label: "Max", MIME: []string{"application/json"}},
			},
			Outputs:       []core.Port{{Port: "result", Label: "Result", MIME: []string{core.MIMEBool}}},
			ParamsSchema:  inRangeSchema,
			Idempotent:    true,
			NoPassthrough: true, // pure predicate — see the binary operators above.
		},
		Execute: executeInRange,
	})
}

// inRangeSchema backs In Range's three literal defaults (for unwired
// Value/Min/Max pins) plus the two inclusive-bound toggles, which stay
// advanced because they default to true — the common case needs neither.
var inRangeSchema = json.RawMessage(`{
	"type":"object",
	"properties":{
		"value":{"type":"string","title":"Value","description":"Literal Value when the Value input isn't wired. Parsed as JSON when possible (e.g. 200)."},
		"min":{"type":"string","title":"Min","description":"Literal lower bound when the Min input isn't wired. Parsed as JSON (e.g. 200)."},
		"max":{"type":"string","title":"Max","description":"Literal upper bound when the Max input isn't wired. Parsed as JSON (e.g. 299)."},
		"inclusive_min":{"type":"boolean","default":true,"title":"Inclusive Min","description":"Include the lower bound. Defaults to true (like Unreal's InRange).","x_advanced":true},
		"inclusive_max":{"type":"boolean","default":true,"title":"Inclusive Max","description":"Include the upper bound. Defaults to true (like Unreal's InRange).","x_advanced":true}
	}
}`)

// executeInRange resolves the three operands (wired pin, else literal param),
// packs Min/Max into the [min, max] list the in_range op expects, and reuses
// Compare's evaluate so range semantics live in exactly one place.
func executeInRange(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	value := operand(job, "value")
	b := []any{operand(job, "min"), operand(job, "max")}

	matched, err := evaluate(value, b, "in_range",
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
