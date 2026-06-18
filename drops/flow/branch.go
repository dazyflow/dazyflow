package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
	"git.sr.ht/~klahr/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "branch",
			Version:     "2.0",
			Label:       "Branch",
			Icon:        "git-branch",
			Category:    "flow_control",
			Provider:    "internal",
			Tags:        []string{"conditional", "routing", "if-else"},
			Description: "Route the payload on the 'in' port to either the then or else output, based on the boolean 'condition' input. Produce that boolean with a Compare drop (wire its result into condition) — true sends the payload down then, false (or a missing/empty condition) sends it down else. Nodes wired to the unused port stay dormant.",
			Summary:     "Forward the input down the then or else port based on a boolean condition input.",
			Examples: []core.ParamsExample{
				{
					Title:  "Branch is wired, not configured",
					Params: json.RawMessage(`{}`),
					Notes:  "Feed a Compare result into 'condition' and the value to route into 'in'. The test lives in the Compare drop, not here.",
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			// The check is split out (Unreal-Blueprint style): Branch is a
			// pure router. 'condition' is the boolean decision (from a Compare
			// drop or any boolean-emitting node); 'in' is the payload that
			// continues down the chosen port.
			Inputs: []core.Port{
				{Port: "condition", Required: true, Label: "Condition", MIME: []string{core.MIMEBool}},
				{Port: "in", Required: true, Label: "Value"},
			},
			Outputs: []core.Port{
				{Port: "then", Label: "True"},
				{Port: "else", Label: "False"},
			},
			ParamsSchema: json.RawMessage(`{"type":"object"}`),
			Idempotent:   true,
			// Pure router: pass would emit on every success regardless of which
			// port the payload took, so a node wired to it would fire on BOTH
			// branches — defeating the routing. Route via 'in' → then/else.
			NoPassthrough: true,
		},
		Execute: executeBranch,
	})
}

// executeBranch is a pure router: it forwards the 'in' payload down "then" or
// "else" depending on the boolean "condition" input — never both. Downstream
// nodes wired to the unused port are dormant (the engine treats a
// missing-port-output as a skip-blocking edge).
//
// Why no inline condition? The comparison logic lives in the Compare drop
// (Unreal-Blueprint splits the check from the branch). Composing Compare →
// Branch keeps each node single-purpose and lets conditions grow arbitrarily
// complex without bloating the router.
func executeBranch(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	payload, ok := job.Input["in"]
	if !ok {
		return params.Err(job, "missing_input", "input port 'in' is required"), nil
	}
	condRef, ok := job.Input["condition"]
	if !ok {
		return params.Err(job, "missing_input", "input port 'condition' is required — wire a boolean (e.g. a Compare result) into it"), nil
	}
	cond, err := asBool(condRef)
	if err != nil {
		return params.Err(job, "bad_input", err.Error()), nil
	}

	output := map[string]core.Ref{}
	if cond {
		output["then"] = payload
	} else {
		output["else"] = payload
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: output,
	}, nil
}

// asBool coerces a condition ref into a boolean. It accepts a native bool
// (the usual case, from Compare), the strings "true"/"false"/"1"/"0"/"yes"/
// "no" (and JSON-encoded booleans), and treats numbers as truthy when nonzero.
// A nil/absent inline value is false, so an unfired upstream routes to else.
func asBool(ref core.Ref) (bool, error) {
	switch v := ref.Inline.(type) {
	case bool:
		return v, nil
	case nil:
		return false, nil
	case float64:
		return v != 0, nil
	case float32:
		return v != 0, nil
	case int:
		return v != 0, nil
	case int64:
		return v != 0, nil
	case json.Number:
		f, err := v.Float64()
		if err != nil {
			return false, fmt.Errorf("condition %q is not a number", v.String())
		}
		return f != 0, nil
	case string:
		switch strings.TrimSpace(strings.ToLower(v)) {
		case "true", "1", "yes":
			return true, nil
		case "false", "0", "no", "":
			return false, nil
		}
		var b bool
		if err := json.Unmarshal([]byte(v), &b); err == nil {
			return b, nil
		}
		return false, fmt.Errorf("condition is a string %q that isn't a boolean", v)
	default:
		return false, fmt.Errorf("condition input must be a boolean, got %T", ref.Inline)
	}
}
