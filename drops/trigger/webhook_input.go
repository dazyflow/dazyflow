// Package trigger contains modules whose execution depends on an
// external trigger event. They register normal Execute handlers that
// produce a clear error when run without their corresponding trigger,
// so graph authors get a fast "you need a webhook" signal instead of
// silent zero-value behavior.
package trigger

import (
	"context"
	"encoding/json"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "webhook_input",
			Version:     "1.0",
			Label:       "Webhook",
			Icon:        "webhook",
			Category:    "trigger",
			Provider:    "internal",
			Tags:        []string{"webhook", "trigger", "http", "event"},
			Description: "Starts the flow when something is sent to its web address — a submission from the flow's hosted form, or an HTTP request from another system. Data is what was sent (form fields / JSON); Headers carries the request's metadata.",
			Summary:     "Starts the flow when its form is submitted or its web address receives data.",
			Examples: []core.ParamsExample{
				{
					Title:  "Webhook input (no params)",
					Params: json.RawMessage(`{}`),
					Notes:  "This node has no params — the trigger URL is provisioned per graph and the body/headers come from the inbound request.",
				},
			},
			ExecutionModel: core.ExecutionTrigger,
			ProcessModel:   core.ProcessLongLived,
			// No inputs — webhook is the data source.
			Outputs: []core.Port{
				{Port: "body", Label: "Data"},
				{Port: "headers", Label: "Headers", MIME: []string{"application/json"}},
			},
			// Webhook + hosted-form config lives on the node now (like the
			// Schedule/Poll nodes), read by the daemon's /trigger and /form
			// handlers. secret guards the POST endpoint; public_form opts into a
			// token-less hosted form whose fields/title are set here too.
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"secret":{
						"type":"string",
						"title":"Secret",
						"description":"Bearer token callers must send (Authorization: Bearer …) to POST this flow's /trigger endpoint. Leave blank only if you rely solely on a public hosted form."
					},
					"public_form":{
						"type":"boolean",
						"title":"Public hosted form",
						"description":"Also expose a public intake form at /form/<tenant>/<workspace>/<id> — no token required (possession of the URL is the credential)."
					},
					"form_fields":{
						"type":"array",
						"items":{"type":"string"},
						"title":"Form fields",
						"description":"Field names the hosted form collects. Defaults to name, email, message."
					},
					"form_title":{
						"type":"string",
						"title":"Form title",
						"description":"Heading shown on the hosted form. Defaults to the flow's name."
					}
				}
			}`),
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
			Message: "nothing was sent to this flow — it starts from its form or web address; submit the form (or POST the trigger URL) instead of pressing Run",
		},
	}, nil
}
