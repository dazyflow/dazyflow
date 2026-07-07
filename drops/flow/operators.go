// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package flow

import (
	"context"
	"encoding/json"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/engine"
)

// Primitive comparison drops — the "super basic" split of the Compare node,
// one node per operator, modelled on Unreal Blueprint's individual
// comparison nodes (==, !=, >, >=, <, <=). Each bakes a fixed operator and
// delegates to compareWith (compare.go), so there is exactly one evaluator:
// a primitive can never drift from Compare's semantics — it IS Compare, with
// the operator as the node's identity instead of a dropdown.
//
// They share Compare's shape — two operand pins A and B (wire them, or type a
// literal default), and a Yes/No output emitting a boolean to feed Branch. The win is
// reading speed: an "A > B" node says what it does at a glance, no need to
// open it and read the dropdown. The full Compare stays the power node for
// the long tail (contains, one_of, in_range, exists) — the primitives are
// deliberately only the binary comparisons that earn their glance-value.
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
			Outputs:        []core.Port{{Port: "result", Label: "Yes/No", MIME: []string{core.MIMEBool}}},
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
		desc:    "Emit true on the Yes/No output when A equals B, otherwise false. The atomic equality step — wire A and B, or type literal defaults. Pair the Yes/No output with Branch to route. Reach for Compare instead when you need richer tests (contains, one_of, ranges).",
		example: core.ParamsExample{Title: "Status equals 200", Params: json.RawMessage(`{"B":200}`), Notes: "Wire the status into A; B is the literal 200. It's true when A == 200."},
	})
	registerOperator(operatorSpec{
		id: "neq", label: "A ≠ B", icon: "equal-not", op: "not_equals",
		summary: "Emit true when A does not equal B, else false.",
		desc:    "Emit true on the Yes/No output when A does not equal B, otherwise false. Pair the Yes/No output with Branch to route.",
		example: core.ParamsExample{Title: "State is not idle", Params: json.RawMessage(`{"B":"idle"}`), Notes: `It's true when A is anything other than "idle".`},
	})
	registerOperator(operatorSpec{
		id: "gt", label: "A > B", icon: "chevron-right", op: "greater_than", numeric: true,
		summary: "Emit true when A is greater than B, else false.",
		desc:    "Emit true on the Yes/No output when numeric A is strictly greater than B, otherwise false. Both operands must be numbers. Pair the Yes/No output with Branch to route.",
		example: core.ParamsExample{Title: "Over a threshold", Params: json.RawMessage(`{"B":1000}`), Notes: "Wire the number into A; B is the literal 1000. It's true when A > 1000."},
	})
	registerOperator(operatorSpec{
		id: "gte", label: "A ≥ B", icon: "chevrons-right", op: "greater_or_equal", numeric: true,
		summary: "Emit true when A is greater than or equal to B, else false.",
		desc:    "Emit true on the Yes/No output when numeric A is greater than or equal to B, otherwise false. Pair the Yes/No output with Branch to route.",
		example: core.ParamsExample{Title: "At least N items", Params: json.RawMessage(`{"B":3}`), Notes: "It's true when A >= 3."},
	})
	registerOperator(operatorSpec{
		id: "lt", label: "A < B", icon: "chevron-left", op: "less_than", numeric: true,
		summary: "Emit true when A is less than B, else false.",
		desc:    "Emit true on the Yes/No output when numeric A is strictly less than B, otherwise false. Both operands must be numbers. Pair the Yes/No output with Branch to route.",
		example: core.ParamsExample{Title: "Under a limit", Params: json.RawMessage(`{"B":100}`), Notes: "It's true when A < 100."},
	})
	registerOperator(operatorSpec{
		id: "lte", label: "A ≤ B", icon: "chevrons-left", op: "less_or_equal", numeric: true,
		summary: "Emit true when A is less than or equal to B, else false.",
		desc:    "Emit true on the Yes/No output when numeric A is less than or equal to B, otherwise false. Pair the Yes/No output with Branch to route.",
		example: core.ParamsExample{Title: "No more than N", Params: json.RawMessage(`{"B":5}`), Notes: "It's true when A <= 5."},
	})
}
