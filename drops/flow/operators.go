package flow

import (
	"context"
	"encoding/json"

	"git.sr.ht/~klahr/hazyflow/core"
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
// literal default), and a Result port emitting 1/0 to feed Branch. The win is
// reading speed: an "A > B" node says what it does at a glance, no need to
// open it and read the dropdown. The full Compare stays the power node for
// the long tail (contains, one_of, in_range, exists).
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
	summary string
	desc    string
	example core.ParamsExample
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
			Inputs:         []core.Port{{Port: "A", Label: "A"}, {Port: "B", Label: "B"}},
			Outputs:        []core.Port{{Port: "result", Label: "Result", MIME: []string{"application/json"}}},
			ParamsSchema:   operandSchema,
			Idempotent:     true,
		},
		Execute: func(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
			return compareWith(job, o.op)
		},
	})
}

func init() {
	registerOperator(operatorSpec{
		id: "eq", label: "A = B", icon: "equal", op: "equals",
		summary: "Emit 1 when A equals B, else 0.",
		desc:    "Emit 1 (true) on Result when A equals B, otherwise 0. The atomic equality node — wire A and B, or type literal defaults. Pair Result with Branch to route. Reach for Compare instead when you need richer tests (contains, one_of, ranges).",
		example: core.ParamsExample{Title: "Status equals 200", Params: json.RawMessage(`{"B":200}`), Notes: "Wire the status into A; B is the literal 200. Result is 1 when A == 200."},
	})
	registerOperator(operatorSpec{
		id: "neq", label: "A ≠ B", icon: "equal-not", op: "not_equals",
		summary: "Emit 1 when A does not equal B, else 0.",
		desc:    "Emit 1 (true) on Result when A does not equal B, otherwise 0. Pair Result with Branch to route.",
		example: core.ParamsExample{Title: "State is not idle", Params: json.RawMessage(`{"B":"idle"}`), Notes: `Result is 1 when A is anything other than "idle".`},
	})
	registerOperator(operatorSpec{
		id: "gt", label: "A > B", icon: "chevron-right", op: "greater_than",
		summary: "Emit 1 when A is greater than B, else 0.",
		desc:    "Emit 1 (true) on Result when numeric A is strictly greater than B, otherwise 0. Both operands must be numbers. Pair Result with Branch to route.",
		example: core.ParamsExample{Title: "Over a threshold", Params: json.RawMessage(`{"B":1000}`), Notes: "Wire the number into A; B is the literal 1000. Result is 1 when A > 1000."},
	})
	registerOperator(operatorSpec{
		id: "gte", label: "A ≥ B", icon: "chevrons-right", op: "greater_or_equal",
		summary: "Emit 1 when A is greater than or equal to B, else 0.",
		desc:    "Emit 1 (true) on Result when numeric A is greater than or equal to B, otherwise 0. Pair Result with Branch to route.",
		example: core.ParamsExample{Title: "At least N items", Params: json.RawMessage(`{"B":3}`), Notes: "Result is 1 when A >= 3."},
	})
	registerOperator(operatorSpec{
		id: "lt", label: "A < B", icon: "chevron-left", op: "less_than",
		summary: "Emit 1 when A is less than B, else 0.",
		desc:    "Emit 1 (true) on Result when numeric A is strictly less than B, otherwise 0. Both operands must be numbers. Pair Result with Branch to route.",
		example: core.ParamsExample{Title: "Under a limit", Params: json.RawMessage(`{"B":100}`), Notes: "Result is 1 when A < 100."},
	})
	registerOperator(operatorSpec{
		id: "lte", label: "A ≤ B", icon: "chevrons-left", op: "less_or_equal",
		summary: "Emit 1 when A is less than or equal to B, else 0.",
		desc:    "Emit 1 (true) on Result when numeric A is less than or equal to B, otherwise 0. Pair Result with Branch to route.",
		example: core.ParamsExample{Title: "No more than N", Params: json.RawMessage(`{"B":5}`), Notes: "Result is 1 when A <= 5."},
	})
}
