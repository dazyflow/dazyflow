package flow

import (
	"context"
	"encoding/json"

	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:             "await_approval",
			Version:        "1.0",
			Label:          "Await approval",
			Color:          "#5a9bd4",
			Icon:           "user-check",
			Category:       "flow_control",
			Provider:       "internal",
			Tags:           []string{"human_in_the_loop", "approval", "pause", "wait"},
			Description:    "Pause the graph until an external HTTP approval arrives. Emits the approval URL on `pending_url` so downstream nodes (email, Slack) can notify a human. On resume, emits the decision on `decision`, plus a control signal on either `approved` or `rejected` — wire downstream `then`/`else` branches accordingly.",
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{{
				Port:  "context",
				Label: "Optional context payload (passed through to downstream)",
			}},
			Outputs: []core.Port{
				{Port: "pending_url", Label: "Approval URL (emitted during pause)"},
				{Port: "decision", Label: "approve | reject"},
				{Port: "approver", Label: "Subject string from the resume call"},
				{Port: "comment", Label: "Optional free-text from the resume call"},
				{Port: "approved", Label: "Control signal — fires when decision=approve"},
				{Port: "rejected", Label: "Control signal — fires when decision=reject"},
				{Port: "context", Label: "Pass-through of the context input"},
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
	if ctxRef, ok := job.Input["context"]; ok {
		output["context"] = ctxRef
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusAwaiting,
		Output: output,
	}, nil
}
