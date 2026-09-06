// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

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
			ID:          "request_input",
			Version:     "1.0",
			Label:       "Request",
			Icon:        "arrow-left-right",
			Category:    "trigger",
			Provider:    "internal",
			Tags:        []string{"request", "api", "http", "reply", "response", "trigger"},
			Description: "Starts the flow when another system calls its address AND waits for an answer. Body is what was sent (JSON or text); Headers carries the request's metadata. Pair it with a Reply step — what Reply produces is sent back as the response. Use the Webhook step instead when the caller only notifies you and wants an immediate acknowledgement.",
			Summary:     "Starts the flow when a system asks it something, and answers with your Reply step.",
			Examples: []core.ParamsExample{
				{
					Title:  "Request trigger with one key",
					Params: json.RawMessage(`{"secrets":["${secret.FLOW_REQUEST_KEY}"]}`),
					Notes:  "Callers POST the flow's /call address with Authorization: Bearer <key>. The address is provisioned per flow; the body and headers come from the inbound request.",
				},
			},
			ExecutionModel: core.ExecutionTrigger,
			ProcessModel:   core.ProcessLongLived,
			// No inputs — the inbound request is the data source.
			Outputs: []core.Port{
				{Port: "body", Label: "Body"},
				{Port: "headers", Label: "Headers", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"secrets":{
						"type":"array",
						"items":{"type":"string"},
						"title":"Secret keys",
						"description":"Bearer tokens callers may send (Authorization: Bearer …) to POST this flow's /call endpoint. The endpoint accepts ANY listed key, so you can add a new key, migrate callers, then revoke the old one with zero downtime. With no key the endpoint rejects every call."
					}
				}
			}`),
			// Same reasoning as webhook_input: retry of a trigger is
			// meaningless — the seed value can't be re-derived.
			Idempotent: false,
		},
		Execute: executeRequestInput,
	})
}

// executeRequestInput runs only when a graph containing this node is run
// WITHOUT an inbound request (the editor's Run button, a schedule). The
// /call handler pre-completes the node's JobRecord directly, so this never
// runs on the real path.
func executeRequestInput(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusError,
		Error: &core.JobError{
			Code:    "no_trigger_data",
			Message: "nothing was sent to this flow — it starts when a system calls its address and waits for a reply; call that address instead of pressing Run",
		},
	}, nil
}
