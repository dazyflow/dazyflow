package flow

import (
	"context"
	"encoding/json"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "await_approval",
			Version:     "1.0",
			Label:       "Await approval",
			Icon:        "user-check",
			Category:    "flow_control",
			Provider:    "internal",
			Tags:        []string{"human_in_the_loop", "approval", "pause", "wait"},
			Description: "Pause the graph until an external HTTP approval arrives. Emits the approval URL on `pending_url` so downstream nodes (email, Slack) can notify a human. On resume, routes the input `Value` out the `Approved` or `Rejected` port matching the decision (wire each to its follow-up — Branch-style, no separate Branch node needed), alongside the `Approver` who decided and their `Comment`.",
			Summary:     "Park the flow until a human hits the approve or reject link, then route downstream by decision.",
			Examples: []core.ParamsExample{
				{
					Title:  "Ask a manager to approve a refund",
					Params: json.RawMessage(`{"prompt":"Refund $230 to customer #4821 — order shipped damaged. Approve?"}`),
					Notes:  "Wire pending_url into an email or Slack drop so the approver gets the link.",
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
				{Port: "pending_url", Label: "Approval URL", MIME: []string{"text/plain"}},
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
					"prompt":{"type":"string"}
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
