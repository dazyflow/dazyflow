// Package value contains literal/constant source drops — nodes that
// take no input and emit a graph-author-supplied value on their
// output port. Useful for inline prompts, templates, snippets, and
// other small bits of data that don't deserve a workspace file.
package value

import (
	"context"
	"encoding/json"

	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:             "text",
			Version:        "1.0",
			Label:          "Text",
			Color:          "#888",
			Icon:           "text",
			Category:       "transformation",
			Provider:       "internal",
			Tags:           []string{"text", "string", "constant", "literal"},
			Description:    "Emit a literal string value. The 'text' param can be multi-line; downstream consumers see it as text/plain on the 'out' port.",
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Outputs:        []core.Port{{Port: "out", Label: "Text"}},
			ParamsSchema:   json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`),
			Idempotent:     true,
		},
		Execute: executeText,
	})
}

func executeText(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	text, _ := job.Params["text"].(string)
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"out": {MIME: "text/plain", Inline: text},
		},
	}, nil
}
