// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package flow

import (
	"context"
	"encoding/json"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "await_approval",
			Version:     "1.0",
			Label:       "Wait for approval",
			Icon:        "user-check",
			Category:    "flow_control",
			Provider:    "internal",
			Tags:        []string{"human_in_the_loop", "approval", "pause", "wait"},
			Description: "Pause the flow until someone approves. Fill in 'Email these people' and Dazyflow mails them the approval link when the flow gets here, and tells them the outcome once it is decided; leave it blank and no mail is sent. To notify some other way — or instead — put this step BEFORE a notify step — it hands you an `Approval link` (the `pending_url` output) to put in that message (e.g. ntfy's 'Link to open', or an email body). Anyone who has the link can approve or reject — there's no per-person targeting, so send it only to the people who should decide; the flow records who clicked on the `Approver` output. The person taps the link to approve or reject; only then does the rest of the flow continue. On resume, the input `Value` comes out the `Approved` or `Rejected` port matching the decision (connect each to its follow-up — no separate Branch needed), alongside the `Approver` who decided and their `Comment`.",
			Summary:     "Pause until a person approves: optionally email them a link, then continue on their decision.",
			Examples: []core.ParamsExample{
				{
					Title:  "Notify on ntfy, then approve before sending",
					Params: json.RawMessage(`{"prompt":"A reply is ready to send. Approve?"}`),
					Notes:  "Connect the draft into Value; connect pending_url (Approval link) into the ntfy step's 'Link to open'; connect the Approved port into your send step.",
				},
				{
					Title:  "Ask a manager to approve a refund",
					Params: json.RawMessage(`{"prompt":"Refund $230 to customer #4821 — order shipped damaged. Approve?"}`),
					Notes:  "Connect pending_url into an email or Slack step so the approver gets the link.",
				},
				{
					Title:  "Gate a production deploy",
					Params: json.RawMessage(`{"prompt":"Promote build 1.42.0 to production?"}`),
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			// await_approval threads its input Value across the pause itself:
			// Execute stashes it on the Awaiting result, and
			// daemon.Service.Approve routes it back out the Approved or Rejected
			// port that matches the decision (Branch-style). The universal
			// `pass` pin would be redundant AND wouldn't work here —
			// engine.ApplyPassthrough only fires on StatusOK, never on this
			// node's Awaiting result. So opt out of it.
			NoPassthrough: true,
			Inputs: []core.Port{{
				// Untyped: the value can be anything the author wants to carry
				// across the approval to downstream nodes. Port id stays
				// "context" (daemon.Approve routes it out the taken decision
				// port on resume); label is Value.
				Port:  "context",
				Label: "Value",
			}},
			Outputs: []core.Port{
				{Port: "pending_url", Label: "Approval link", MIME: []string{"text/plain"}},
				// Branch-style decision ports: the input Value rides out exactly
				// one of these — `approved` on approve, `rejected` on reject — so
				// downstream edges fork on the decision by port presence, the
				// same mechanism Branch's then/else uses, with no separate Branch
				// node needed. Untyped: they carry whatever was threaded in.
				{Port: "approved", Label: "Approved"},
				{Port: "rejected", Label: "Rejected"},
				// The authenticated subject that made the decision.
				{Port: "approver", Label: "Approver", MIME: []string{"text/plain"}},
				{Port: "comment", Label: "Comment", MIME: []string{"text/plain"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"prompt":{
						"type":"string",
						"title":"Question to ask",
						"description":"The question shown on the approval page — e.g. 'A reply is ready to send. Approve?'. Note: anyone who opens the Approval link can approve or reject; the link is the only key, so share it only with the people who should decide. The flow records who clicked on the Approver output.",
						"examples":["A reply is ready to send. Approve?"]
					},
					"approvers":{
						"type":"string",
						"title":"Email these people",
						"description":"Comma-separated email addresses to notify when the flow reaches this step, and again once someone decides. Leave blank and no email is sent — deliver the Approval link yourself, or let people work the Approvals inbox. The email carries the same Approval link the pending_url output does — anyone who opens it can decide, so list only the people who should.",
						"examples":["ops@acme.se, manager@acme.se"]
					}
				}
			}`),
			Idempotent:     true,
			AwaitsApproval: true,
		},
		Execute: executeAwaitApproval,
	})
}

// executeAwaitApproval is the "pause" path. The module never produces a
// terminal decision on its own — it emits an awaiting Result that the
// worker translates into a JobStatusAwaiting record and parks. The
// resume path lives in daemon.Service.Approve, which writes a fresh
// Result with the human's decision and re-triggers downstream dispatch.
//
// The first execution emits the approval URL on pending_url so a
// downstream notification node could pick it up. Re-executions (if a
// worker dies and the lease expires before the record is parked) re-emit
// the same URL because the signer is deterministic on graphRunID:nodeID.
func executeAwaitApproval(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	prompt, _ := job.Params["prompt"].(string)
	output := map[string]core.Ref{
		"pending_url": {MIME: "text/plain", Inline: job.ApprovalURL},
	}
	if prompt != "" {
		output["prompt"] = core.Ref{MIME: "text/plain", Inline: prompt}
	}
	// Stash the carried Value on the awaiting result. The decision isn't
	// known yet, so it can't go out approved/rejected here — Service.Approve
	// routes it onto the taken port once the human decides.
	if ctxRef, ok := job.Input["context"]; ok {
		output["context"] = ctxRef
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusAwaiting,
		Output: output,
	}, nil
}
