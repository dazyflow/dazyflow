// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package flow

import (
	"context"
	"encoding/json"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "reply",
			Version:     "1.0",
			Label:       "Reply",
			Icon:        "corner-up-left",
			Category:    "flow_control",
			Provider:    "internal",
			Tags:        []string{"reply", "response", "answer", "request", "api"},
			Description: "Answers whoever called this flow's Request address, then lets the flow carry on — so a caller with a short timeout can be acknowledged before the slow steps run. Wire the value into Body, or type it into the param; a wired value keeps its own type (a list or record is sent as JSON), typed text is sent as text. Only the first Reply a run reaches answers; when a run reaches none, the caller gets the run's status instead.",
			Summary:     "Answer the caller that started this flow, then keep going.",
			Examples: []core.ParamsExample{
				{
					Title:  "Acknowledge the caller before the slow work",
					Params: json.RawMessage(`{"body":"Got it — processing order ${trigger.body.order_id}."}`),
					Notes:  "Place it right after the Request step; the caller is answered immediately and the rest of the flow keeps running.",
				},
				{
					Title:  "Answer with a computed value",
					Params: json.RawMessage(`{"status_code":201}`),
					Notes:  "Leave body unset and wire the value into the Body input instead — a record or list is sent back as JSON.",
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs:         []core.Port{{Port: "body", Label: "Reply body"}},
			Outputs:        []core.Port{{Port: "body", Label: "Sent"}},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"body":{
						"type":"string",
						"title":"What to send back",
						"description":"The text the caller receives. Supports ${...} references. Ignored when something is wired into the Body input, which keeps its own type."
					},
					"status_code":{
						"type":"integer",
						"minimum":200,
						"maximum":599,
						"default":200,
						"title":"Response code",
						"description":"The HTTP status the caller receives. 200 unless you need to signal something specific (201 created, 202 accepted, 422 rejected)."
					}
				}
			}`),
			Idempotent: true,
		},
		Execute: executeReply,
	})
}

// executeReply resolves what goes back to the caller and emits it on `body`.
// The /call handler reads that port off this node's record — the step itself
// never touches the connection, so it behaves identically in a run nobody is
// waiting on (a schedule, the editor's Run button), where it simply records
// what would have been sent.
func executeReply(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	if code := params.IntDefault(job.Params, "status_code", core.ReplyDefaultStatus); code < 200 || code > 599 {
		return params.Err(job, "bad_param", "status_code must be between 200 and 599"), nil
	}
	ref, ok := replyBody(job)
	if !ok {
		return params.Err(job, "bad_param", "the Body input carries a value this step can't send back — wire text, a record or a list"), nil
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{"body": ref},
	}, nil
}

// replyBody prefers a wired Body input (keeping the value's own type) and
// falls back to the `body` param as text. ok is false only for a value that
// can't be serialised, which is a wiring mistake worth naming.
func replyBody(job core.Job) (core.Ref, bool) {
	if in, present := job.Input["body"]; present && in.Inline != nil {
		mime := in.MIME
		if mime == "" {
			mime = replyMIME(in.Inline)
		}
		if mime != replyTextMIME {
			if _, err := json.Marshal(in.Inline); err != nil {
				return core.Ref{}, false
			}
		}
		return core.Ref{MIME: mime, Inline: in.Inline, Headers: in.Headers}, true
	}
	return core.Ref{MIME: replyTextMIME, Inline: params.StringDefault(job.Params, "body", "")}, true
}

const replyTextMIME = "text/plain; charset=utf-8"

func replyMIME(v any) string {
	switch v.(type) {
	case string, []byte:
		return replyTextMIME
	default:
		return "application/json"
	}
}
