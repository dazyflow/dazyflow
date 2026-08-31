// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package flow

import (
	"context"
	"encoding/json"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/engine"
)

// Boolean combinators — AND / OR / NOT — the missing third of the Unreal
// Blueprint "logic" set. The comparison operators in operators.go PRODUCE
// booleans (A > B); these COMBINE them, so "A > 10 AND B < 5" no longer needs
// nested Branches — wire both Compare results into an AND and feed its Yes/No output
// into a single Branch.condition.
//
// Like the comparison primitives they sit in the "logic" category, emit a
// boolean on the Yes/No output, and are pure predicates (NoPassthrough). Every input is
// coerced through asBool (branch.go), the same coercion Branch.condition uses,
// so a combinator reads a Compare result, a raw bool, or the truthy
// strings/numbers asBool accepts — one coercion for the whole package, no
// drift.
//
// AND/OR are variadic (wire two or more boolean pins, like Merge's fan-in);
// NOT is unary. None take literal params: a combinator's whole job is to fold
// upstream booleans, so there's nothing to type on the node.

func init() {
	registerCombinator(combinatorSpec{
		id: "and", label: "A AND B", icon: "ampersand", all: true,
		summary: "Emit true on the Yes/No output only when every connected input is true.",
		desc:    "Emit true on the Yes/No output when ALL connected boolean inputs are true, otherwise false (logical AND). Variadic — connect two or more Compare results (or any boolean-emitting steps) and feed the Yes/No output into a single Branch step's condition input. With a single input it just passes that input through; an empty set errors.",
		example: core.ParamsExample{Title: "Over threshold AND in stock", Notes: "Connect one Compare (amount > 1000) and another (stock > 0) into the inputs. It's true only when both are true."},
	})
	registerCombinator(combinatorSpec{
		id: "or", label: "A OR B", icon: "slash", all: false,
		summary: "Emit true on the Yes/No output when any connected input is true.",
		desc:    "Emit true on the Yes/No output when ANY connected boolean input is true, otherwise false (logical OR). Variadic — connect two or more Compare results (or any boolean-emitting steps) and feed the Yes/No output into a single Branch step's condition input. With a single input it just passes that input through; an empty set errors.",
		example: core.ParamsExample{Title: "VIP OR high value", Notes: "Connect one Compare (tier == 'vip') and another (amount > 1000) into the inputs. It's true when either is true."},
	})

	// NOT — the unary member. One boolean input, negated onto the Yes/No output.
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "not",
			Version:     "1.0",
			Label:       "NOT",
			Icon:        "ban",
			Category:    "logic",
			Provider:    "internal",
			Tags:        []string{"condition", "predicate", "boolean", "logic", "combinator", "not", "negate", "invert"},
			Description: "Emit the logical negation of the connected boolean input on the Yes/No output — true becomes false, false becomes true. Connect a Compare result (or any boolean-emitting step) into the input; feed the Yes/No output into a Branch step's condition input to flip which port a payload takes without rewiring the branch.",
			Summary:     "Negate the boolean input: emit true when the input is false, and false when it is true.",
			Examples: []core.ParamsExample{{
				Title: "Branch when NOT approved",
				Notes: "Connect a Compare (status == 'approved') into the input; it's true for every status that isn't 'approved'.",
			}},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs:         []core.Port{{Port: "in", Required: true, Label: "Yes/No value", MIME: []string{core.MIMEBool}}},
			Outputs:        []core.Port{{Port: "result", Label: "Yes/No", MIME: []string{core.MIMEBool}}},
			ParamsSchema:   json.RawMessage(`{"type":"object"}`),
			Idempotent:     true,
			NoPassthrough:  true, // pure predicate — see operators.go.
		},
		Execute: executeNot,
	})
}

// combinatorSpec is the per-drop variation for the variadic AND/OR pair;
// everything else is supplied by registerCombinator. `all` selects the fold:
// true ANDs the inputs, false ORs them.
type combinatorSpec struct {
	id      string
	label   string
	icon    string
	all     bool
	summary string
	desc    string
	example core.ParamsExample
}

func registerCombinator(c combinatorSpec) {
	min := 1
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:       c.id,
			Version:  "1.0",
			Label:    c.label,
			Icon:     c.icon,
			Category: "logic",
			Provider: "internal",
			// Color unset: the UI tints "logic" drops from the category palette,
			// the way Blueprint colors pure nodes (see operators.go).
			Tags:           []string{"condition", "predicate", "boolean", "logic", "combinator", c.id},
			Description:    c.desc,
			Summary:        c.summary,
			Examples:       []core.ParamsExample{c.example},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			// Variadic boolean fan-in, modelled on Merge's items pin: wire two or
			// more boolean pins. Min 1 keeps a single-input node valid (it folds
			// to that input) rather than erroring mid-build.
			Inputs: []core.Port{{
				Port:     "in",
				Label:    "Yes/No values",
				Variadic: true,
				Min:      &min,
				MIME:     []string{core.MIMEBool},
			}},
			Outputs:       []core.Port{{Port: "result", Label: "Yes/No", MIME: []string{core.MIMEBool}}},
			ParamsSchema:  json.RawMessage(`{"type":"object"}`),
			Idempotent:    true,
			NoPassthrough: true, // pure predicate — see operators.go.
		},
		Execute: func(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
			return combine(job, c.all)
		},
	})
}

// combine folds the variadic boolean inputs into a single verdict. `all`=true
// is AND (seed true, every input must hold); `all`=false is OR (seed false,
// any input flips it). Every input is coerced through asBool so the fold reads
// the same boolean shapes Branch.condition accepts. An empty input set is a
// wiring error rather than a silent true/false.
func combine(job core.Job, all bool) (core.Result, error) {
	refs := core.VariadicInputs(job.Input, "in")
	if len(refs) == 0 {
		return params.Err(job, "missing_input", "connect at least one Yes/No value into the inputs pin"), nil
	}
	result := all
	for _, ref := range refs {
		b, err := asBool(ref)
		if err != nil {
			return params.Err(job, "bad_input", err.Error()), nil
		}
		if all {
			result = result && b
		} else {
			result = result || b
		}
	}
	return boolResult(job, result), nil
}

func executeNot(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	ref, ok := job.Input["in"]
	if !ok {
		return params.Err(job, "missing_input", "input port 'in' is required — connect a Yes/No value (e.g. a Compare result) into it"), nil
	}
	b, err := asBool(ref)
	if err != nil {
		return params.Err(job, "bad_input", err.Error()), nil
	}
	return boolResult(job, !b), nil
}

// boolResult builds the single-port boolean Result every logic drop emits.
func boolResult(job core.Job, v bool) core.Result {
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"result": {MIME: core.MIMEBool, Inline: v},
		},
	}
}
