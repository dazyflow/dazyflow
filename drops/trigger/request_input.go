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
			Description: "Starts the flow when another system calls its address AND waits for an answer. Body is what was sent (JSON or text); Headers carries the request's metadata. Pair it with a Reply step — what Reply produces is sent back as the response. Callers prove themselves with one of the keys below, sent either as a header or as ?key=… on the end of the address. Use the Webhook step instead when the caller only notifies you and wants an immediate acknowledgement.",
			Summary:     "Starts the flow when a system asks it something, and answers with your Reply step.",
			Examples: []core.ParamsExample{
				{
					Title:  "Request trigger with one key",
					Params: json.RawMessage(`{"secrets":["${secret.FLOW_REQUEST_KEY}"]}`),
					Notes:  "Callers POST the flow's /call address with Authorization: Bearer <key>. The address is provisioned per flow; the body and headers come from the inbound request.",
				},
				{
					Title:  "Caller that can only paste a URL",
					Params: json.RawMessage(`{"secrets":["${secret.FLOW_REQUEST_KEY}"]}`),
					Notes:  "Same key, sent as ?key=<key> on the end of the /call address — for a system whose settings are a URL box and nothing else.",
				},
				{
					Title:  "Open endpoint",
					Params: json.RawMessage(`{"public":true}`),
					Notes:  "Anyone who knows the address gets the flow's Reply. Last resort, and a bigger decision than opening a Webhook step: this one hands back output, not just a run.",
				},
			},
			ExecutionModel: core.ExecutionTrigger,
			ProcessModel:   core.ProcessLongLived,
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
						"description":"Keys a caller may send to call this flow, either as an Authorization: Bearer header or as ?key=… on the end of the address — use the address form for systems that only let you paste a URL. The endpoint accepts ANY listed key, so you can add a new key, migrate callers, then revoke the old one with zero downtime."
					},
					"public":{
						"type":"boolean",
						"title":"Answer calls with no key",
						"default":false,
						"description":"Let anyone who knows this flow's address call it, with no key at all. Weigh this more carefully than on a Webhook step: this address answers, so an open one hands your Reply to whoever asks. Only for a caller that can send neither a header nor a key in the URL. Off by default, and a key-less step stays inert until you turn this on."
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
