// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package value

import (
	"context"
	"encoding/json"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "number",
			Version:     "1.0",
			Label:       "Number",
			Color:       "#888",
			Icon:        "hash",
			Category:    "transformation",
			Provider:    "internal",
			Tags:        []string{"number", "numeric", "constant", "literal", "int", "float"},
			Description: "Emit a literal numeric value. Later steps see it as a JSON number on the 'out' port — connect it into a comparison (In Range, Compare), an operator, or a numeric input like a Delay step's duration.",
			Summary:     "Emit a fixed number you type on the step, on the 'out' port.",
			Examples: []core.ParamsExample{
				{
					Title:  "Integer constant",
					Params: json.RawMessage(`{"value":200}`),
				},
				{
					Title:  "Decimal threshold",
					Params: json.RawMessage(`{"value":0.95}`),
					Notes:  "Feed into In Range's Min/Max or a Compare to gate on a numeric threshold.",
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			// A literal value source: its output IS the `value` param, so it
			// originates data and takes no pass pin (you can't wire into a
			// literal). See core.WithPassthrough / Manifest.ValueSource.
			ValueSource:  true,
			Outputs:      []core.Port{{Port: "out", Label: "Number", MIME: []string{"application/json"}}},
			ParamsSchema: json.RawMessage(`{"type":"object","properties":{"value":{"type":"number","title":"Number","description":"The number to emit (e.g. 200 or 0.95)."}},"required":["value"]}`),
			Idempotent:   true,
		},
		Execute: executeNumber,
	})
}

func executeNumber(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	v, ok := toNumber(job.Params["value"])
	if !ok {
		return core.Result{
			JobID:  job.ID,
			Status: core.StatusError,
			Error:  &core.JobError{Code: "bad_param", Message: "value must be a number"},
		}, nil
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			// Emitted as a JSON number so it wires straight into the numeric
			// ports (In Range, Compare, operators, Delay ms), whose coercion
			// accepts float64/int/json.Number.
			"out": {MIME: "application/json", Inline: v},
		},
	}, nil
}

// toNumber normalises the param value to a float64. JSON decoding yields
// float64 for plain numbers and json.Number when the decoder is in
// UseNumber mode; ints cover programmatically-built params. Anything else
// (string, nil, bool) is rejected so a mistyped literal surfaces as a clear
// error rather than silently emitting 0.
func toNumber(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}
