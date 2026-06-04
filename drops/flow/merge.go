package flow

import (
	"context"
	"encoding/json"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/engine"
)

func init() {
	min := 1
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "merge",
			Version:     "1.0",
			Label:       "Merge",
			Icon:        "git-merge",
			Category:    "flow_control",
			Provider:    "internal",
			Tags:        []string{"fan_in", "aggregate", "join"},
			Description: "Wait for N upstream inputs to arrive and emit them as a single list on the out port. Useful as a synchronization point in parallel branches.",
			Summary:     "Synchronize parallel branches by collecting every upstream input into a single list emitted on out.",
			Examples: []core.ParamsExample{
				{
					Title:  "No configuration — merge takes no params",
					Params: json.RawMessage(`{}`),
					Notes:  "Wire two or more upstream output ports into the variadic items input.",
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{{
				Port:     "items",
				Label:    "Inputs",
				Variadic: true,
				Min:      &min,
			}},
			Outputs: []core.Port{{
				Port:  "out",
				Label: "Merged list",
				MIME:  []string{MIMEList},
			}},
			ParamsSchema: json.RawMessage(`{"type":"object"}`),
			Idempotent:   true,
		},
		Execute: executeMerge,
	})
}

// MIMEList marks a Ref whose Inline field carries a []core.Ref. Downstream
// modules can either consume the list via Inline or split it before reading.
const MIMEList = "application/x-hazyflow-list+json"

func executeMerge(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	refs := core.VariadicInputs(job.Input, "items")
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"out": {MIME: MIMEList, Inline: refs},
		},
	}, nil
}
