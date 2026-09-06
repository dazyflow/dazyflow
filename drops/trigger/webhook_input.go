// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package trigger contains modules whose execution depends on an
// external trigger event. They register normal Execute handlers that
// produce a clear error when run without their corresponding trigger,
// so graph authors get a fast "you need a webhook" signal instead of
// silent zero-value behavior.
package trigger

import (
	"context"
	"encoding/json"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/engine"
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
			Description: "Starts the flow when another system sends something to its web address, and acknowledges the delivery straight away. Body is what was sent (JSON or text); Headers carries the request's metadata. Use the Form step when the sender is a person, and the Request step when the caller waits for an answer.",
			Summary:     "Starts the flow when another system posts to its web address.",
			Examples: []core.ParamsExample{
				{
					Title:  "Webhook with one key",
					Params: json.RawMessage(`{"secrets":["${secret.FLOW_WEBHOOK_KEY}"]}`),
					Notes:  "Senders POST the flow's /trigger address with Authorization: Bearer <key>. The address is provisioned per flow; the body and headers come from the inbound request.",
				},
			},
			ExecutionModel: core.ExecutionTrigger,
			ProcessModel:   core.ProcessLongLived,
			// No inputs — webhook is the data source.
			Outputs: []core.Port{
				{Port: "body", Label: "Body"},
				{Port: "headers", Label: "Headers", MIME: []string{"application/json"}},
			},
			// Webhook config lives on the node (like the Schedule/Poll nodes),
			// read by the daemon's /trigger handler. The hosted form is its own
			// step now: one door per trigger, one contract per address.
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"secrets":{
						"type":"array",
						"items":{"type":"string"},
						"title":"Secret keys",
						"description":"Bearer tokens callers may send (Authorization: Bearer …) to POST this flow's /trigger endpoint. The endpoint accepts ANY listed key, so you can add a new key, migrate callers, then revoke the old one with zero downtime. With no key the endpoint rejects every delivery."
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
			Message: "nothing was sent to this flow — it starts when another system posts to its web address; post to that address instead of pressing Run",
		},
	}, nil
}
