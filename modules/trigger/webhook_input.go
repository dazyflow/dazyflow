// Package trigger contains modules whose execution depends on an
// external trigger event. They register normal Execute handlers that
// produce a clear error when run without their corresponding trigger,
// so graph authors get a fast "you need a webhook" signal instead of
// silent zero-value behavior.
package trigger

import (
	"context"
	"encoding/json"

	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/engine"
)

func init() {
	engine.Register(engine.NativeNode{
		Manifest: core.Manifest{
			ID:             "webhook_input",
			Version:        "1.0",
			Label:          "Webhook input",
			Color:          "#aa66dd",
			Icon:           "webhook",
			Category:       "trigger",
			Provider:       "internal",
			Tags:           []string{"webhook", "trigger", "http", "event"},
			Description:    "Receives the body and headers of the inbound HTTP request that fired this graph. Pre-completed by the daemon when a webhook trigger matches; running the graph manually via 'hzctl graph run' will fail this node with no_trigger_data.",
			ExecutionModel: core.ExecutionTrigger,
			ProcessModel:   core.ProcessLongLived,
			// No inputs — webhook is the data source.
			Outputs: []core.Port{
				{Port: "body", Label: "Request body (string for text MIMEs, parsed object for JSON)"},
				{Port: "headers", Label: "Request headers as a JSON object"},
			},
			ParamsSchema: json.RawMessage(`{"type":"object"}`),
			// Idempotent in the sense that retry is safe — but in
			// practice retry is meaningless: a webhook fires the graph
			// once and the seed value won't be re-derived on a retry.
			// Mark non-idempotent so retry edges fail validation.
			Idempotent: false,
		},
		Execute: executeWebhookInput,
	})
}

// executeWebhookInput is called only when a graph containing this node
// is run WITHOUT a webhook trigger having fired. The webhook path
// pre-completes the node's JobRecord with status=succeeded directly,
// bypassing the worker, so Execute never runs in the trigger flow.
func executeWebhookInput(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusError,
		Error: &core.JobError{
			Code:    "no_trigger_data",
			Message: "webhook_input has no body — this graph must be fired via its webhook trigger (POST /trigger/<tenant>/<workspace>/<graph>), not run manually",
		},
	}, nil
}
