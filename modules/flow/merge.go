package flow

import (
	"context"
	"encoding/json"

	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/engine"
)

func init() {
	min := 1
	engine.Register(engine.NativeNode{
		Manifest: core.Manifest{
			ID:             "merge",
			Version:        "1.0",
			Label:          "Merge",
			Color:          "#5a9bd4",
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
